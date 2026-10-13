package logger

import (
	"encoding/json"
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

// Level はログの重要度だ。
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

// Options は Logger の生成オプションだ。
type Options struct {
	LogFile string
	Level   Level
	Prefix  string
	UseUTC  bool

	// MaxSizeMB はローテーションを起こすサイズ閾値だ。0（または未設定）は既定の
	// 10 MB を使い、負値はローテーションを無効化する。
	// MaxSizeMB is the size threshold that triggers rotation. 0 (or unset)
	// uses the default of 10 MB; a negative value disables rotation.
	MaxSizeMB int
	// MaxBackups は保持するローテーション済みファイル数だ（既定: 5）。
	// MaxBackups is the number of rotated files to keep (default: 5).
	MaxBackups int

	// ForwardURL を設定すると、出力されたすべてのログ行をこの HTTP(S) エンドポイントへ
	// 転送する（1 リクエスト 1 行、ノンブロッキング、有界キュー、リダイレクト拒否）。
	// opt-in で既定は off。pkg/logger は host 専用なので、プラグインのビルドハッシュは変わらない。
	// ForwardURL, when set, mirrors every emitted log line to this HTTP(S)
	// endpoint (one line per request, non-blocking, bounded queue, redirects
	// refused). Opt-in and off by default. pkg/logger is host-only, so this
	// does not change any plugin build hash.
	ForwardURL string
	// ForwardTimeout は 1 回の転送リクエストの上限だ（既定: 5s）。
	// ForwardTimeout bounds one forward request (default: 5s).
	ForwardTimeout time.Duration
}

// Logger は Kizuna-Eye 共通のロガーだ。
type Logger struct {
	mu     sync.Mutex
	logger *log.Logger
	level  Level
	prefix string
	file   *os.File
	writer io.Writer
	// fw を設定すると行をリモートエンドポイントへ転送する（forward.go 参照）。
	// fw, when set, mirrors lines to a remote endpoint (see forward.go).
	fw *forwardWriter
}

// ============================================================
// rotatingWriter はファイルへ書き、maxSize を超えて成長すると
// ローテーションし、最大 maxBackups 個の旧ファイルを保持する。
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
	// ログはユーザー名・IP を含む。ディレクトリは作成時に所有者のみにする。
	// 配備によっては「共有」ログディレクトリを setgid + グループ所有へ緩め、
	// 隔離された agent が書き続けられるようにするが、それは運用者の判断であり、
	// このプロセスが勝手に広げるものではない。
	// Logs contain usernames/IPs; keep the directory owner-only when we create
	// it. A deployment may relax the *shared* log directory to setgid + group
	// ownership so an isolated agent can keep writing there; that is an
	// operator decision, not something this process should broaden on its own.
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
	// world-readable で作られた可能性のある既存ファイルを締める。
	// 0640 は意図的なグループ読み取りビットを保つ。A-4 で agent は専用ユーザーで動き、
	// 運用者（および agent ログを読む dashboard のログビューア）は kizuna-eye が書いた
	// ログを依然として読める必要がある。どのグループかは包含ディレクトリの setgid ビットで決まる。
	// Tighten an existing file that may have been created world-readable.
	// 0640 keeps a deliberate group-read bit: A-4 runs the agent as its own
	// user, and the operator (plus the dashboard's log viewer, which reads the
	// agent log) must still be able to read logs written by kizuna-eye.
	// Which group that is decided by the containing directory's setgid bit.
	_ = os.Chmod(path, 0640)
	return w, nil
}

func (w *rotatingWriter) open() error {
	// ログファイルはユーザー名・IP その他の機微データを含み得るため 0640 で作成する。
	// 書き込みは所有者のみ、読み取りは共有 kizuna-eye グループへ（newRotatingWriter 参照）。
	// プロセスの umask はこれを厳しくできるだけで、広げることはない。
	// Log files may contain usernames, IPs, and other sensitive data, so
	// create them 0640: owner-only for writing, group-read for the shared
	// kizuna-eye group (see newRotatingWriter). The process umask can only
	// make this stricter, never wider.
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0640)
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
			// ローテーション失敗。書き込み可能なハンドルを必ず確保する。
			// さもないとこの行（および以降のすべての行）が閉じたファイルへ書かれ失われる。
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
	// ハンドルを使用不能にし、ローテーション失敗時に Write が再オープンするようにする。
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

	// maxBackups を超えるものを除去する（防御的）。
	// Remove anything beyond maxBackups (defensive).
	w.cleanup()

	return w.open()
}

func (w *rotatingWriter) cleanup() {
	// ローテーション済みファイルは "<path>.1"（最新）.."<path>.N"（最古）と名付ける。
	// 数値サフィックスが maxBackups を超えるファイルを除去する。これにより、
	// 最新ファイルを覆い隠す古いサフィックス（例: path.10 が文字列ソートで path.2 より
	// 古いと扱われる）を残さない。
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
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	// 再オープンも失敗したローテーション失敗後は w.file が nil になる。
	// シャットダウン中の Sync/Close がそれを参照してはならない（nil panic）。
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

	var fw *forwardWriter
	if strings.TrimSpace(opts.ForwardURL) != "" {
		f, ferr := newForwardWriter(opts.ForwardURL, opts.ForwardTimeout)
		if ferr != nil {
			fmt.Fprintf(os.Stderr, "ログ転送の初期化に失敗: %v\n", ferr)
		} else {
			fw = f
			writer = io.MultiWriter(writer, fw)
		}
	}

	flag := 0
	if opts.UseUTC {
		flag |= log.LUTC
	}

	lg := log.New(writer, "", flag)

	out := &Logger{
		logger: lg,
		level:  opts.Level,
		prefix: opts.Prefix,
		file:   file,
		fw:     fw,
	}
	// nil の *rotatingWriter を writer に入れてはいけない。型アサーションは
	// 成功するため Sync/Close が nil レシーバを触り、シャットダウン時に
	// panic する（LogFile 未指定・オープン失敗時に発生）。
	// Never put a nil *rotatingWriter into writer: a type assertion succeeds,
	// so Sync/Close would touch a nil receiver and panic at shutdown (this
	// happens when LogFile is unset or open failed).
	if rot != nil {
		out.writer = rot
	}
	return out
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
		// 既に保持しているロック下で Close する（Close は再ロックしてデッドロックする）。
		// os.Exit は戻らないので unlock は不要だ。
		// Close under the lock we already hold (Close would re-lock and
		// deadlock). os.Exit does not return, so no unlock is needed.
		l.closeLocked()
		os.Exit(1)
	}
}

// logT は両言語のレンダリングを載せた JSON Lines エントリを 1 行書く。
// 基底の "message" は日本語レンダリング（既存の UI 既定）で、"message_en" は英語だ。
// これにより web/static/logs.js が UI 言語に応じて選べる。log() と違い、出力は
// 1 行 1 JSON オブジェクトで、従来の "[LEVEL] [PREFIX] ts msg" テキストではない。
// 移行中は 1 ファイル内で両形式が混在し得る（logs.js は既に許容している）。
// logT writes one JSON Lines entry carrying BOTH language renderings. The
// base "message" is the Japanese rendering (the existing UI default) and
// "message_en" is the English one, so web/static/logs.js can pick per the UI
// language. Unlike log(), the output is a single JSON object per line, not
// the legacy "[LEVEL] [PREFIX] ts msg" text; the two formats may be mixed in
// one file during migration (logs.js already tolerates that).
func (l *Logger) logT(level Level, key string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if level < l.level {
		return
	}

	entry := map[string]interface{}{
		"ts":         time.Now().Format(time.RFC3339),
		"level":      levelNames[level],
		"message":    msg("ja", key, args...),
		"message_en": msg("en", key, args...),
	}
	if l.prefix != "" {
		entry["prefix"] = strings.Trim(l.prefix, "[]")
	}
	data, err := json.Marshal(entry)
	if err != nil {
		// イベントを決して落とさない。最小限のプレーン行にフォールバックする。
		// Never drop the event: fall back to a minimal plain line.
		l.logger.Println(levelNames[level] + " " + key)
	} else {
		l.logger.Println(string(data))
	}

	if level == FATAL {
		l.closeLocked()
		os.Exit(1)
	}
}

func (l *Logger) Debug(format string, args ...interface{}) { l.log(DEBUG, format, args...) }
func (l *Logger) Info(format string, args ...interface{})  { l.log(INFO, format, args...) }
func (l *Logger) Warn(format string, args ...interface{})  { l.log(WARN, format, args...) }
func (l *Logger) Error(format string, args ...interface{}) { l.log(ERROR, format, args...) }
func (l *Logger) Fatal(format string, args ...interface{}) { l.log(FATAL, format, args...) }

// *T メソッドは、リテラルの書式文字列ではなくカタログキー（catalog.go 参照）を取り、
// ja/en 両方のレンダリングを持つ JSON Lines エントリを出力する。
// *T methods take a catalog key (see catalog.go) instead of a literal format
// string and emit a JSON Lines entry with both ja/en renderings.
func (l *Logger) DebugT(key string, args ...interface{}) { l.logT(DEBUG, key, args...) }
func (l *Logger) InfoT(key string, args ...interface{})  { l.logT(INFO, key, args...) }
func (l *Logger) WarnT(key string, args ...interface{})  { l.logT(WARN, key, args...) }
func (l *Logger) ErrorT(key string, args ...interface{}) { l.logT(ERROR, key, args...) }
func (l *Logger) FatalT(key string, args ...interface{}) { l.logT(FATAL, key, args...) }

// Sync はバッファ済みログデータをフラッシュする。ロガーミューテックスを取るため、
// ログ書き込み（同じファイル/ライターに触る）と並行実行できない。
// Sync flushes buffered log data. It takes the logger mutex so it cannot run
// concurrently with a log write (which would touch the same file/writer).
func (l *Logger) Sync() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if rw, ok := l.writer.(*rotatingWriter); ok && rw != nil {
		return rw.Sync()
	}
	if l.file != nil {
		return l.file.Sync()
	}
	return nil
}

// Close はログファイルを閉じる。Sync と同じ理由でロガーミューテックスを取る。
// FATAL 経路は既に保持しているため closeLocked を直接呼ぶ。
// Close closes the log file. It takes the logger mutex for the same reason as
// Sync. The FATAL path calls closeLocked directly because it already holds it.
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closeLocked()
}

// closeLocked は実際の close を行う。呼び出し側は l.mu を保持すること。
// closeLocked performs the actual close. Caller must hold l.mu.
func (l *Logger) closeLocked() {
	if rw, ok := l.writer.(*rotatingWriter); ok && rw != nil {
		_ = rw.Close()
	} else if l.file != nil {
		l.file.Close()
	}
	if l.fw != nil {
		_ = l.fw.Close()
	}
}

// ParseLevel は文字列を Level へ変換する。不明な値は INFO を返す。
// ParseLevel converts a string to a Level. Unknown values return INFO.
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
