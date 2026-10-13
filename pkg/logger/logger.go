package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Level int

const (
	DEBUG Level = iota
	INFO
	WARN
	ERROR
	FATAL
)

var levelNames = map[Level]string{
	DEBUG: "DEBUG",
	INFO:  "INFO",
	WARN:  "WARN",
	ERROR: "ERROR",
	FATAL: "FATAL",
}

type Options struct {
	LogFile string
	Level   Level
	Prefix  string
	UseUTC  bool

	// MaxSizeMB is the size threshold that triggers rotation. 0 (or unset)
	// uses the default of 10 MB; a negative value disables rotation.
	MaxSizeMB int
	// MaxBackups is the number of rotated files to keep (default: 5).
	MaxBackups int
}

type Logger struct {
	mu     sync.Mutex
	logger *log.Logger
	level  Level
	prefix string
	file   *os.File
	writer io.Writer
}

// ============================================================
// rotatingWriter writes to a file and rotates it when it grows
// past maxSize, keeping at most maxBackups old files.
// ============================================================
type rotatingWriter struct {
	mu         sync.Mutex
	path       string
	file       *os.File
	size       int64
	maxSize    int64
	maxBackups int
}

func newRotatingWriter(path string, maxSize int64, maxBackups int) (*rotatingWriter, error) {
	// Logs contain usernames/IPs; keep the directory owner-only.
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	w := &rotatingWriter{
		path:       path,
		maxSize:    maxSize,
		maxBackups: maxBackups,
	}
	if err := w.open(); err != nil {
		return nil, err
	}
	// Tighten an existing file that may have been created world-readable.
	_ = os.Chmod(path, 0600)
	return w, nil
}

func (w *rotatingWriter) open() error {
	// Log files may contain usernames, IPs, and other sensitive data, so
	// create them 0600 (owner only).
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.file = f
	w.size = info.Size()
	return nil
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.maxSize > 0 && w.size+int64(len(p)) > w.maxSize {
		if err := w.rotate(); err != nil {
			// Rotation failed. Make sure we still have a writable handle,
			// otherwise the log line (and every line after it) would be
			// written to a closed file and lost.
			fmt.Fprintf(os.Stderr, "ログローテーション失敗: %v\n", err)
			if w.file == nil {
				if err := w.open(); err != nil {
					return 0, err
				}
			}
		}
	}

	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingWriter) rotate() error {
	// Mark the handle unusable so Write reopens on a rotation failure.
	if err := w.file.Close(); err != nil {
		w.file = nil
		return err
	}
	w.file = nil

	// path -> path.1, path.1 -> path.2, ...
	for i := w.maxBackups - 1; i >= 1; i-- {
		old := fmt.Sprintf("%s.%d", w.path, i)
		newer := fmt.Sprintf("%s.%d", w.path, i+1)
		if _, err := os.Stat(old); err == nil {
			_ = os.Rename(old, newer)
		}
	}
	_ = os.Rename(w.path, w.path+".1")

	// Remove anything beyond maxBackups (defensive).
	w.cleanup()

	return w.open()
}

func (w *rotatingWriter) cleanup() {
	// Rotated files are named "<path>.1" (newest) .. "<path>.N" (oldest).
	// Remove any file whose numeric suffix exceeds maxBackups, so we never
	// keep a stale suffix that would otherwise shadow the newest file
	// (e.g. path.10 being treated as older than path.2 by a string sort).
	matches, _ := filepath.Glob(w.path + ".*")
	prefix := filepath.Base(w.path) + "."
	for _, m := range matches {
		base := filepath.Base(m)
		if !strings.HasPrefix(base, prefix) {
			continue
		}
		n, err := strconv.Atoi(base[len(prefix):])
		if err != nil {
			continue
		}
		if n > w.maxBackups {
			_ = os.Remove(m)
		}
	}
}

func (w *rotatingWriter) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	// w.file is nil after a failed rotation whose reopen also failed, so a
	// Sync/Close during shutdown must not dereference it (nil panic).
	if w.file == nil {
		return nil
	}
	return w.file.Sync()
}

func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	return w.file.Close()
}

// ============================================================
// Logger
// ============================================================
func NewLogger(opts *Options) *Logger {
	if opts == nil {
		opts = &Options{Level: INFO}
	}

	maxSizeMB := opts.MaxSizeMB
	if maxSizeMB == 0 {
		maxSizeMB = 10
	}
	maxBackups := opts.MaxBackups
	if maxBackups <= 0 {
		maxBackups = 5
	}

	var writer io.Writer = os.Stdout
	var file *os.File
	var rot *rotatingWriter

	if opts.LogFile != "" {
		r, err := newRotatingWriter(opts.LogFile, int64(maxSizeMB)*1024*1024, maxBackups)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ログファイルオープン失敗: %v\n", err)
		} else {
			rot = r
			file = r.file
			writer = io.MultiWriter(os.Stdout, r)
		}
	}

	flag := 0
	if opts.UseUTC {
		flag |= log.LUTC
	}

	lg := log.New(writer, "", flag)

	return &Logger{
		logger: lg,
		level:  opts.Level,
		prefix: opts.Prefix,
		file:   file,
		writer: rot,
	}
}

func (l *Logger) log(level Level, format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if level < l.level {
		return
	}

	msg := fmt.Sprintf(format, args...)
	timestamp := time.Now().Format("2006-01-02 15:04:05.000")

	var prefix string
	if l.prefix != "" {
		prefix = fmt.Sprintf("[%s] %s %s ", levelNames[level], l.prefix, timestamp)
	} else {
		prefix = fmt.Sprintf("[%s] %s ", levelNames[level], timestamp)
	}

	l.logger.Println(prefix + msg)

	if level == FATAL {
		// Close under the lock we already hold (Close would re-lock and
		// deadlock). os.Exit does not return, so no unlock is needed.
		l.closeLocked()
		os.Exit(1)
	}
}

func (l *Logger) Debug(format string, args ...interface{}) { l.log(DEBUG, format, args...) }
func (l *Logger) Info(format string, args ...interface{})  { l.log(INFO, format, args...) }
func (l *Logger) Warn(format string, args ...interface{})  { l.log(WARN, format, args...) }
func (l *Logger) Error(format string, args ...interface{}) { l.log(ERROR, format, args...) }
func (l *Logger) Fatal(format string, args ...interface{}) { l.log(FATAL, format, args...) }

// Sync flushes buffered log data. It takes the logger mutex so it cannot run
// concurrently with a log write (which would touch the same file/writer).
func (l *Logger) Sync() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if rw, ok := l.writer.(*rotatingWriter); ok {
		return rw.Sync()
	}
	if l.file != nil {
		return l.file.Sync()
	}
	return nil
}

// Close closes the log file. It takes the logger mutex for the same reason as
// Sync. The FATAL path calls closeLocked directly because it already holds it.
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closeLocked()
}

// closeLocked performs the actual close. Caller must hold l.mu.
func (l *Logger) closeLocked() {
	if rw, ok := l.writer.(*rotatingWriter); ok {
		_ = rw.Close()
		return
	}
	if l.file != nil {
		l.file.Close()
	}
}

func ParseLevel(s string) Level {
	switch s {
	case "debug":
		return DEBUG
	case "info":
		return INFO
	case "warn":
		return WARN
	case "error":
		return ERROR
	case "fatal":
		return FATAL
	default:
		return INFO
	}
}
