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

	// extra holds additional log files that live outside logDir, keyed by
	// the id the UI requests (?type=<id>). Plugin logs are written under
	// their own directories (e.g. /samba/share/Kizuna-.../logs/), which is
	// outside the dashboard's logDir, so they must be registered explicitly.
	extra map[string]string

	// extraFn, when set, supplies the extra logs on every request. modules.json
	// can change while the dashboard runs (a plugin is added or removed in the
	// UI), so a one-time snapshot would keep listing a removed module or miss
	// a newly added one until the dashboard restarts.
	extraFn func() map[string]string
}

// SetExtraLogsFunc installs a callback that supplies the extra logs (plugin
// logs) on every request. Use this instead of SetExtraLogs when modules.json
// can change while the dashboard is running.
func (l *LogHandler) SetExtraLogsFunc(fn func() map[string]string) {
	l.extraFn = fn
}

// NewLogHandler creates a LogHandler.
func NewLogHandler(agentLogFile, dashboardLogFile, logDir string) *LogHandler {
	return &LogHandler{
		agentLogFile:     agentLogFile,
		dashboardLogFile: dashboardLogFile,
		logDir:           logDir,
		extra:            make(map[string]string),
	}
}

// SetExtraLogs replaces the extra log registry. ids must match logNameRe so
// they can be safely used as ?type=<id>; paths are made absolute later.
func (l *LogHandler) SetExtraLogs(extra map[string]string) {
	if l.extra == nil {
		l.extra = make(map[string]string)
	}
	for id, path := range extra {
		if id == "" || path == "" || !logNameRe.MatchString(id) {
			continue
		}
		l.extra[id] = path
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
	if !logNameRe.MatchString(logType) {
		return "", false
	}
	// Extra logs (plugin logs outside logDir) take precedence: they are
	// registered explicitly, so an id that matches one is served even though
	// it is not under logDir. Re-read the callback so a removed module's log
	// stops resolving immediately.
	if l.extraFn != nil {
		if p, ok := l.extraFn()[logType]; ok {
			return l.safeExtraLogPath(p)
		}
	}
	if p, ok := l.extra[logType]; ok {
		return l.safeExtraLogPath(p)
	}
	if l.logDir == "" {
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
// directory, plus the explicitly registered extra logs. The built-in
// agent/dashboard logs are implied and not listed here.
type logTypeInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

func (l *LogHandler) handleLogTypes(w http.ResponseWriter, r *http.Request) {
	types := []logTypeInfo{}

	// List only the logs of currently registered modules. The dashboard
	// supplies each plugin's log_path (SetExtraLogs/SetExtraLogsFunc), so
	// this list follows modules.json even if it changes while the dashboard
	// runs. Scanning logDir instead would surface leftover files from
	// removed plugins and show the operator "modules" that are not
	// installed, and would list the same file twice under two ids.
	ids := make(map[string]bool)
	if l.extraFn != nil {
		for id := range l.extraFn() {
			ids[id] = true
		}
	}
	for id := range l.extra {
		ids[id] = true
	}
	extraIDs := make([]string, 0, len(ids))
	for id := range ids {
		extraIDs = append(extraIDs, id)
	}
	sort.Strings(extraIDs)
	for _, id := range extraIDs {
		types = append(types, logTypeInfo{ID: id, Label: id})
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"types": types})
}

// safeExtraLogPath validates an explicitly registered extra log. The file may
// live outside logDir, so only the symlink and regular-file rules from
// safeLogPath apply; the containment check is skipped on purpose.
func (l *LogHandler) safeExtraLogPath(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// Missing or unreadable. Reject so the caller never reads an
		// unresolved symlink.
		return "", false
	}
	info, serr := os.Stat(real)
	if serr != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return filepath.Clean(real), true
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
		// エラー本文の改行も SSE の行終端になるため除去する。
		msg := strings.NewReplacer("\r", " ", "\n", " ").Replace(err.Error())
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", msg)
		return
	}
	for _, line := range strings.Split(tail, "\n") {
		if line == "" {
			continue
		}
		// A bare CR is an SSE line terminator too, so an embedded '\r'
		// in a log line could split it and inject a forged "data:" line
		// into the viewer. Strip CR so each log line stays one SSE event.
		fmt.Fprintf(w, "data: %s\n\n", strings.ReplaceAll(strings.TrimRight(line, "\r"), "\r", ""))
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
				// エラー本文の改行も SSE の行終端になるため除去する。
				msg := strings.NewReplacer("\r", " ", "\n", " ").Replace(err.Error())
				fmt.Fprintf(w, "event: error\ndata: %s\n\n", msg)
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

	// Read and send the rest. bufio.Reader.ReadString/ReadBytes track exact
	// byte counts for CRLF files; Scanner would strip '\r' and make position
	// tracking wrong.
	//
	// The reader buffer is bounded, but a single line is NOT: ReadBytes keeps
	// accumulating until it finds '\n', so a log with a very long newline-less
	// span (a runaway plugin writing one enormous line) would be read into
	// memory in full and could exhaust the dashboard. ReadString is therefore
	// used with a per-line byte cap: once the cap is hit the rest of the line
	// is discarded (the stream still advances by the exact byte count, so the
	// position stays correct and the next poll resumes after the line).
	const maxStreamLineBytes = 1 << 20 // 1 MiB per line
	reader := bufio.NewReaderSize(file, 64*1024)
	var newPosition int64 = position
	for {
		line, readErr := readLineCapped(reader, maxStreamLineBytes)
		// Only consume complete lines (terminated by '\n'). A partial line at
		// EOF is left for the next poll, otherwise its tail would be sent
		// twice: once now and again as part of the completed line later.
		if readErr == nil && line.complete {
			// Strip CR anywhere in the line (a bare CR is an SSE line
			// terminator; leaving one in would let a log line inject a
			// forged "data:" event into the viewer).
			text := strings.ReplaceAll(strings.TrimRight(line.text, "\r\n"), "\r", "")
			fmt.Fprintf(w, "data: %s\n\n", text)
			newPosition += line.consumed
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

// streamLine is one line read from the log stream. consumed is the exact
// number of bytes taken from the reader (including the '\n' when present), so
// the caller can advance its position without re-reading. complete is true
// when a '\n' terminated the line; a capped but unterminated line sets it to
// false so the caller does not emit a partial line as if it were whole.
type streamLine struct {
	text     string
	consumed int64
	complete bool
}

// readLineCapped reads one line from r, up to max bytes. If the line exceeds
// max before a '\n' is found, the remainder is discarded (drained to the next
// '\n' or EOF) and complete=false is returned: the bytes are still counted in
// consumed so the caller's position stays exact and no data is re-sent.
func readLineCapped(r *bufio.Reader, max int) (streamLine, error) {
	var (
		buf      []byte
		consumed int64
	)
	for {
		b, err := r.ReadByte()
		if err != nil {
			// EOF (or a read error) with no more bytes.
			return streamLine{text: string(buf), consumed: consumed, complete: false}, err
		}
		consumed++
		if b == '\n' {
			buf = append(buf, b)
			return streamLine{text: string(buf), consumed: consumed, complete: true}, nil
		}
		if len(buf) < max {
			buf = append(buf, b)
			continue
		}
		// Cap reached: drain the rest of the line so the next call starts at a
		// line boundary, counting the discarded bytes.
		for {
			d, derr := r.ReadByte()
			if derr != nil {
				return streamLine{text: string(buf), consumed: consumed, complete: false}, derr
			}
			consumed++
			if d == '\n' {
				return streamLine{text: string(buf), consumed: consumed, complete: true}, nil
			}
		}
	}
}
