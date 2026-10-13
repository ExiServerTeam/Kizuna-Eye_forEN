package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
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
}

type Logger struct {
	mu     sync.Mutex
	logger *log.Logger
	level  Level
	prefix string
	file   *os.File
}

func NewLogger(opts *Options) *Logger {
	if opts == nil {
		opts = &Options{Level: INFO}
	}

	var writer io.Writer = os.Stdout
	var file *os.File

	if opts.LogFile != "" {
		dir := filepath.Dir(opts.LogFile)
		if err := os.MkdirAll(dir, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "ログディレクトリ作成失敗: %v\n", err)
		} else {
			f, err := os.OpenFile(opts.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
			if err == nil {
				writer = io.MultiWriter(os.Stdout, f)
				file = f
			} else {
				fmt.Fprintf(os.Stderr, "ログファイルオープン失敗: %v\n", err)
			}
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
		l.Close()
		os.Exit(1)
	}
}

func (l *Logger) Debug(format string, args ...interface{}) { l.log(DEBUG, format, args...) }
func (l *Logger) Info(format string, args ...interface{})  { l.log(INFO, format, args...) }
func (l *Logger) Warn(format string, args ...interface{})  { l.log(WARN, format, args...) }
func (l *Logger) Error(format string, args ...interface{}) { l.log(ERROR, format, args...) }
func (l *Logger) Fatal(format string, args ...interface{}) { l.log(FATAL, format, args...) }

func (l *Logger) Sync() error {
	if l.file != nil {
		return l.file.Sync()
	}
	return nil
}

func (l *Logger) Close() {
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
