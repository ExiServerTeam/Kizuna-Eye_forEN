package api

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// LogHandler serves log APIs.
type LogHandler struct {
	agentLogFile     string
	dashboardLogFile string
	logDir           string
}

// NewLogHandler creates a LogHandler.
func NewLogHandler(agentLogFile, dashboardLogFile, logDir string) *LogHandler {
	return &LogHandler{
		agentLogFile:     agentLogFile,
		dashboardLogFile: dashboardLogFile,
		logDir:           logDir,
	}
}

// RegisterRoutes registers the log routes.
func (l *LogHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/logs", l.handleLogs)
	mux.HandleFunc("GET /api/logs/stream", l.handleLogStream)
}

// handleLogs returns the tail of a log file.
func (l *LogHandler) handleLogs(w http.ResponseWriter, r *http.Request) {
	logType := r.URL.Query().Get("type")     // "agent" or "dashboard"
	linesParam := r.URL.Query().Get("lines") // line limit (default: 100)

	var logPath string
	switch logType {
	case "agent":
		logPath = l.agentLogFile
	case "dashboard":
		logPath = l.dashboardLogFile
	default:
		writeJSONError(w, http.StatusBadRequest, "Invalid log type. Use 'agent' or 'dashboard'")
		return
	}

	lines := 100
	if linesParam != "" {
		if n, err := strconv.Atoi(linesParam); err == nil && n > 0 {
			lines = n
		}
	}
	// Cap the line count to avoid excessive memory use.
	const maxLogLines = 10000
	if lines > maxLogLines {
		lines = maxLogLines
	}

	logContent, err := l.readLastLines(logPath, lines)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to read log file: %v", err))
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(logContent))
}

// handleLogStream streams logs via Server-Sent Events.
func (l *LogHandler) handleLogStream(w http.ResponseWriter, r *http.Request) {
	logType := r.URL.Query().Get("type")

	var logPath string
	switch logType {
	case "agent":
		logPath = l.agentLogFile
	case "dashboard":
		logPath = l.dashboardLogFile
	default:
		writeJSONError(w, http.StatusBadRequest, "Invalid log type. Use 'agent' or 'dashboard'")
		return
	}

	// SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Tell nginx (and similar proxies) not to buffer this response,
	// otherwise log lines are delivered in bursts instead of live.
	w.Header().Set("X-Accel-Buffering", "no")

	// Send only the tail of the existing log. Dumping the whole file would
	// send megabytes on every SSE connection and let a large log stall the
	// client. New lines are streamed from the end below.
	const initialLines = 200
	tail, err := l.readLastLines(logPath, initialLines)
	if err != nil {
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", err.Error())
		return
	}
	for _, line := range strings.Split(tail, "\n") {
		if line == "" {
			continue
		}
		fmt.Fprintf(w, "data: %s\n\n", line)
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	// Start streaming from the current end of the file.
	var lastPosition int64
	if info, statErr := os.Stat(logPath); statErr == nil {
		lastPosition = info.Size()
	}

	// Watch for new logs (simple polling).
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			currentPosition, err := l.sendFileFromPosition(w, logPath, lastPosition)
			if err != nil {
				fmt.Fprintf(w, "event: error\ndata: %s\n\n", err.Error())
				return
			}
			lastPosition = currentPosition
		}
	}
}

// readLastLines reads the last N lines of a file.
// It reads backwards in blocks instead of loading the whole file.
func (l *LogHandler) readLastLines(filePath string, n int) (string, error) {
	if n <= 0 {
		return "", nil
	}

	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil // missing file -> empty
		}
		return "", err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	size := info.Size()
	if size == 0 {
		return "", nil
	}

	const blockSize = 64 * 1024
	var (
		newlineCount int
		offset       = size
		buf          []byte
	)

	// Read from the end until n newlines are found.
	for offset > 0 && newlineCount <= n {
		readSize := int64(blockSize)
		if offset < readSize {
			readSize = offset
		}
		offset -= readSize

		chunk := make([]byte, readSize)
		if _, err := file.ReadAt(chunk, offset); err != nil {
			return "", err
		}
		buf = append(chunk, buf...)

		newlineCount = bytes.Count(buf, []byte{'\n'})
	}

	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	return strings.Join(lines, "\n"), nil
}

// sendFileFromPosition sends file content from a position via SSE.
func (l *LogHandler) sendFileFromPosition(w http.ResponseWriter, filePath string, position int64) (int64, error) {
	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return position, nil // missing file -> keep position
		}
		return position, err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return position, err
	}

	// Detect log rotation/truncation: if the file is now smaller than our
	// position, it was rotated. Restart from the beginning so new lines keep
	// streaming; otherwise the stream would silently stop forever.
	if stat.Size() < position {
		position = 0
	}

	if stat.Size() <= position {
		return position, nil // no new logs
	}

	// Seek to the position.
	_, err = file.Seek(position, io.SeekStart)
	if err != nil {
		return position, err
	}

	// Read and send the rest.
	// Use bufio.Reader with ReadBytes('\n') so the exact byte count of each
	// line is tracked, including "\r\n" endings. bufio.Scanner strips "\r",
	// which made the previous position tracking inaccurate for CRLF files
	// and caused duplicated or missing lines in the stream.
	reader := bufio.NewReaderSize(file, 64*1024)
	var newPosition int64 = position
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			text := strings.TrimRight(string(line), "\r\n")
			fmt.Fprintf(w, "data: %s\n\n", text)
			newPosition += int64(len(line))
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return newPosition, readErr
		}
	}

	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	return newPosition, nil
}
