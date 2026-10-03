package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
	// Lists log files available in the log directory (e.g. plugin logs), so the
	// UI can offer a selector for each one instead of hardcoding file names.
	mux.HandleFunc("GET /api/logs/types", l.handleLogTypes)
}

// logNameRe matches a safe log file base name. Only these may be requested by
// name, so a request can never traverse out of the log directory.
var logNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// resolveLogPath maps a log type to a file path. "agent" and "dashboard" are
// the two built-in logs; any other name resolves to <logDir>/<name>.log when
// the name is safe. ok is false for an unsafe or unknown name.
func (l *LogHandler) resolveLogPath(logType string) (string, bool) {
	switch logType {
	case "agent":
		return l.safeLogPath(l.agentLogFile)
	case "dashboard":
		return l.safeLogPath(l.dashboardLogFile)
	}
	if !logNameRe.MatchString(logType) || l.logDir == "" {
		return "", false
	}
	return l.safeLogPath(filepath.Join(l.logDir, logType+".log"))
}

// safeLogPath resolves symlinks in path and rejects anything whose real target
// is not a regular file inside the log directory. The lexical checks elsewhere
// do not follow symlinks, so a symlink planted in the log directory (which the
// agent user can write) would otherwise let a logged-in user read any file the
// agent can read (e.g. logs/evil.log -> /etc/passwd). The real path is also
// required to be a regular file, so devices and directories are refused.
func (l *LogHandler) safeLogPath(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	// Absolutize before resolving: logDir is configured as a relative path
	// ("logs"), and EvalSymlinks/Rel only compare correctly on absolute
	// paths. Without this, Rel could compute a path that does not start with
	// ".." even though the target is outside the directory.
	absDir, aerr := filepath.Abs(l.logDir)
	if aerr != nil {
		absDir = l.logDir
	}
	realDir, err := filepath.EvalSymlinks(absDir)
	if err != nil {
		realDir = absDir
	}

	absPath, aerr2 := filepath.Abs(path)
	if aerr2 != nil {
		absPath = path
	}
	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		// The file does not exist yet (or is unreadable). Reject so the
		// caller does not read an unresolved symlink.
		return "", false
	}
	// Only enforce containment for files under logDir: the agent/dashboard
	// logs may be configured with a path outside it.
	realDirClean := filepath.Clean(realDir)
	realPathClean := filepath.Clean(realPath)
	underDir := realPathClean == realDirClean
	if !underDir {
		if rel, rerr := filepath.Rel(realDirClean, realPathClean); rerr != nil ||
			rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			// Not under logDir. Allow only if it is one of the two configured
			// built-in logs (which may legitimately live elsewhere).
			if !l.isBuiltinLog(realPathClean) {
				return "", false
			}
		}
	}
	if info, serr := os.Stat(realPathClean); serr != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return realPathClean, true
}

// isBuiltinLog reports whether realPath is the configured agent or dashboard
// log file (these may be configured outside logDir).
func (l *LogHandler) isBuiltinLog(realPath string) bool {
	for _, p := range []string{l.agentLogFile, l.dashboardLogFile} {
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		rp, err := filepath.EvalSymlinks(abs)
		if err != nil {
			rp = abs
		}
		if filepath.Clean(rp) == realPath {
			return true
		}
	}
	return false
}

// handleLogTypes lists extra log files (plugin logs, etc.) found in the log
// directory, newest name last. The built-in agent/dashboard logs are implied
// and not listed here.
type logTypeInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

func (l *LogHandler) handleLogTypes(w http.ResponseWriter, r *http.Request) {
	types := []logTypeInfo{}
	if l.logDir != "" {
		entries, err := os.ReadDir(l.logDir)
		if err == nil {
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
					continue
				}
				base := strings.TrimSuffix(e.Name(), ".log")
				// Skip rotated files (agent.log.1 is a different suffix already,
				// but a plain *.log with dots is not a valid type id) and the
				// built-in logs.
				if base == "agent" || base == "dashboard" || !logNameRe.MatchString(base) {
					continue
				}
				names = append(names, base)
			}
			sort.Strings(names)
			for _, n := range names {
				types = append(types, logTypeInfo{ID: n, Label: n})
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"types": types})
}

// handleLogs returns the tail of a log file.
func (l *LogHandler) handleLogs(w http.ResponseWriter, r *http.Request) {
	logType := r.URL.Query().Get("type")
	linesParam := r.URL.Query().Get("lines")

	logPath, ok := l.resolveLogPath(logType)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "Invalid log type")
		return
	}

	lines := 100
	if linesParam != "" {
		if n, err := strconv.Atoi(linesParam); err == nil && n > 0 {
			lines = n
		}
	}
	// Cap the line count to avoid excessive memory use. The UI keeps at most
	// 500 lines, so this is generous.
	const maxLogLines = 1000
	if lines > maxLogLines {
		lines = maxLogLines
	}

	logContent, _, err := l.readLastLines(logPath, lines)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to read log file: %v", err))
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	// Log lines can contain attacker-controlled text (usernames, paths).
	// text/plain is not sniffed as HTML by modern browsers, but nosniff makes
	// that guarantee explicit so a log line can never be interpreted as markup.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(logContent))
}

// handleLogStream streams logs via Server-Sent Events.
func (l *LogHandler) handleLogStream(w http.ResponseWriter, r *http.Request) {
	logType := r.URL.Query().Get("type")

	logPath, ok := l.resolveLogPath(logType)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "Invalid log type")
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
	tail, endPos, err := l.readLastLines(logPath, initialLines)
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

	// Start streaming from the end of what the tail covered.
	lastPosition := endPos

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
// The returned int64 is the file size (byte offset) the read covered, so a
// caller can continue streaming from exactly there without missing or
// duplicating lines.
func (l *LogHandler) readLastLines(filePath string, n int) (string, int64, error) {
	if n <= 0 {
		return "", 0, nil
	}

	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", 0, nil // missing file -> empty
		}
		return "", 0, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	size := info.Size()
	if size == 0 {
		return "", 0, nil
	}

	const blockSize = 64 * 1024
	// Hard cap on how much of the file is read into memory. Without it, a
	// large file with few (or zero) newlines would be read in full, letting
	// a huge log exhaust memory. 4 MiB is far more than the requested line
	// count ever needs.
	const maxBytes = 4 << 20
	var (
		newlineCount int
		offset       = size
		buf          []byte
	)

	// Read from the end until n newlines are found (or the byte cap is hit).
	for offset > 0 && newlineCount <= n && len(buf) < maxBytes {
		readSize := int64(blockSize)
		if offset < readSize {
			readSize = offset
		}
		if int64(len(buf))+readSize > maxBytes {
			readSize = maxBytes - int64(len(buf))
		}
		if readSize <= 0 {
			break
		}
		offset -= readSize

		chunk := make([]byte, readSize)
		if _, err := file.ReadAt(chunk, offset); err != nil {
			return "", 0, err
		}
		buf = append(chunk, buf...)

		newlineCount = bytes.Count(buf, []byte{'\n'})
	}

	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	return strings.Join(lines, "\n"), size, nil
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

	_, err = file.Seek(position, io.SeekStart)
	if err != nil {
		return position, err
	}

	// Read and send the rest. ReadBytes('\n') tracks exact byte counts for
	// CRLF files; Scanner would strip '\r' and make position tracking wrong.
	reader := bufio.NewReaderSize(file, 64*1024)
	var newPosition int64 = position
	for {
		line, readErr := reader.ReadBytes('\n')
		// Only consume complete lines (terminated by '\n'). A partial line at
		// EOF is left for the next poll, otherwise its tail would be sent
		// twice: once now and again as part of the completed line later.
		if readErr == nil && len(line) > 0 {
			text := strings.TrimRight(string(line), "\r\n")
			fmt.Fprintf(w, "data: %s\n\n", text)
			newPosition += int64(len(line))
			continue
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return newPosition, readErr
		}
	}

	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	return newPosition, nil
}
