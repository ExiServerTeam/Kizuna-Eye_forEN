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

// LogHandler はログ API を提供する。
// LogHandler serves log APIs.
type LogHandler struct {
	agentLogFile     string
	dashboardLogFile string
	logDir           string

	// extra は logDir の外にある追加ログファイルを保持し、UI が要求する id
	// (?type=<id>) をキーとする。プラグインログは専用ディレクトリ
	// （例: /samba/share/Kizuna-.../logs/）に書かれ、dashboard の logDir の外に
	// あるため、明示的に登録しなければならない。
	// extra holds additional log files that live outside logDir, keyed by
	// the id the UI requests (?type=<id>). Plugin logs are written under
	// their own directories (e.g. /samba/share/Kizuna-.../logs/), which is
	// outside the dashboard's logDir, so they must be registered explicitly.
	extra map[string]string

	// extraFn を設定すると、リクエストごとに追加ログを供給する。modules.json は
	// dashboard の実行中に変わり得る（UI でプラグインが追加・削除される）ため、
	// 一度きりのスナップショットでは削除済みモジュールを列挙し続けたり、新規追加を
	// dashboard 再起動まで見逃したりする。
	// extraFn, when set, supplies the extra logs on every request. modules.json
	// can change while the dashboard runs (a plugin is added or removed in the
	// UI), so a one-time snapshot would keep listing a removed module or miss
	// a newly added one until the dashboard restarts.
	extraFn func() map[string]string

	// extraAllowedPrefixes は追加ログ（log_path）に許可する絶対ディレクトリの
	// 接頭辞だ。空でなければ、解決後の実パスがこのいずれかの配下でなければ
	// 拒否する。log_path は admin 限定だが、admin が「dashboard 実行ユーザーが
	// 読める任意ファイル」をログ UI 経由で読めるのを防ぐ（多層防御）。
	// extraAllowedPrefixes are the absolute directory prefixes an extra log
	// (log_path) may resolve into. When non-empty, a resolved path outside all
	// of them is refused. log_path is admin-only, but this stops an admin from
	// reading any file the dashboard user can read via the log UI (defense in
	// depth).
	extraAllowedPrefixes []string
}

// SetExtraLogAllowedPrefixes は追加ログに許可する絶対ディレクトリ接頭辞を設定する。
// 空でなければ、解決後の実パスがこのいずれかの配下でなければ安全でないとみなす。
// SetExtraLogAllowedPrefixes sets the absolute directory prefixes an extra
// log may resolve into. When non-empty, a resolved path outside all of them
// is considered unsafe.
func (l *LogHandler) SetExtraLogAllowedPrefixes(prefixes ...string) {
	l.extraAllowedPrefixes = prefixes
}

// SetExtraLogsFunc は追加ログ（プラグインログ）を毎リクエスト供給するコールバックを
// 設定する。modules.json が dashboard の実行中に変わり得る場合は SetExtraLogs ではなく
// これを使う。
// SetExtraLogsFunc installs a callback that supplies the extra logs (plugin
// logs) on every request. Use this instead of SetExtraLogs when modules.json
// can change while the dashboard is running.
func (l *LogHandler) SetExtraLogsFunc(fn func() map[string]string) {
	l.extraFn = fn
}

// NewLogHandler は LogHandler を作る。
// NewLogHandler creates a LogHandler.
func NewLogHandler(agentLogFile, dashboardLogFile, logDir string) *LogHandler {
	return &LogHandler{
		agentLogFile:     agentLogFile,
		dashboardLogFile: dashboardLogFile,
		logDir:           logDir,
		extra:            make(map[string]string),
	}
}

// SetExtraLogs は追加ログの登録を置き換える。id は logNameRe に一致しなければ
// ならない（?type=<id> として安全に使えるように）。パスは後で絶対化される。
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

// RegisterRoutes はログのルートを登録する。
// RegisterRoutes registers the log routes.
func (l *LogHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/logs", l.handleLogs)
	mux.HandleFunc("GET /api/logs/stream", l.handleLogStream)
	// ログディレクトリで利用可能なログファイル（例: プラグインログ）を列挙し、
	// UI がファイル名をハードコードせずに各々のセレクタを出せるようにする。
	// Lists log files available in the log directory (e.g. plugin logs), so the
	// UI can offer a selector for each one instead of hardcoding file names.
	mux.HandleFunc("GET /api/logs/types", l.handleLogTypes)
}

// logNameRe は安全なログファイルのベース名に一致する。名前で要求できるのはこれら
// だけなので、リクエストがログディレクトリの外へトラバースすることは決してない。
// logNameRe matches a safe log file base name. Only these may be requested by
// name, so a request can never traverse out of the log directory.
var logNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// resolveLogPath はログタイプをファイルパスへ対応付ける。"agent" と "dashboard" は
// 組み込みの 2 ログで、それ以外の名前は安全なら <logDir>/<name>.log へ解決する。
// 安全でない・未知の名前では ok が false になる。
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
	// 追加ログ（logDir 外のプラグインログ）を優先する。明示的に登録されているため、
	// logDir の下になくても id が一致すれば提供される。コールバックを再読し、
	// 削除済みモジュールのログが即座に解決されなくなるようにする。
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

// safeLogPath は path 内のシンボリックリンクを解決し、実体がログディレクトリ内の
// 通常ファイルでないものを拒否する。他所の字句チェックはシンボリックリンクを辿らない
// ため、ログディレクトリ（agent ユーザーが書き込める）に仕込まれたシンボリックリンクが
// あると、ログイン済みユーザーが agent の読める任意ファイルを読めてしまっていた
// （例: logs/evil.log -> /etc/passwd）。実体パスは通常ファイルであることも要求し、
// デバイスやディレクトリを拒否する。
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
	// 解決の前に絶対化する。logDir は相対パス（"logs"）で設定され、
	// EvalSymlinks/Rel は絶対パスでのみ正しく比較する。これが無いと、対象が
	// ディレクトリ外でも Rel が ".." で始まらないパスを計算し得る。
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
		// ファイルがまだ存在しない（または読めない）。呼び出し側が未解決の
		// シンボリックリンクを読まないよう拒否する。
		// The file does not exist yet (or is unreadable). Reject so the
		// caller does not read an unresolved symlink.
		return "", false
	}
	// logDir 下のファイルにのみ包含を強制する。agent/dashboard ログは logDir の外の
	// パスで設定され得る。
	// Only enforce containment for files under logDir: the agent/dashboard
	// logs may be configured with a path outside it.
	realDirClean := filepath.Clean(realDir)
	realPathClean := filepath.Clean(realPath)
	underDir := realPathClean == realDirClean
	if !underDir {
		if rel, rerr := filepath.Rel(realDirClean, realPathClean); rerr != nil ||
			rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			// logDir の下ではない。設定された 2 つの組み込みログ（正当に他所に
			// 置かれ得る）のいずれかである場合にのみ許可する。
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

// isBuiltinLog は realPath が設定された agent または dashboard ログファイルかを返す
// （これらは logDir の外に設定され得る）。
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

// handleLogTypes はログディレクトリで見つかった追加ログファイル（プラグインログ等）と、
// 明示的に登録された追加ログを列挙する。組み込みの agent/dashboard ログは暗黙で、
// ここには列挙しない。
// handleLogTypes lists extra log files (plugin logs, etc.) found in the log
// directory, plus the explicitly registered extra logs. The built-in
// agent/dashboard logs are implied and not listed here.
type logTypeInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

func (l *LogHandler) handleLogTypes(w http.ResponseWriter, r *http.Request) {
	types := []logTypeInfo{}

	// 現在登録されているモジュールのログのみを列挙する。dashboard は各プラグインの
	// log_path（SetExtraLogs/SetExtraLogsFunc）を供給するため、この一覧は dashboard の
	// 実行中に modules.json が変わっても追随する。logDir を走査すると代わりに、
	// 削除済みプラグインの残骸ファイルが現れ、未インストールの「モジュール」を
	// 運用者に見せてしまい、同じファイルを 2 つの id で二重に列挙することになる。
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

// safeExtraLogPath は明示的に登録された追加ログを検証する。ファイルは logDir の外に
// あり得るため、safeLogPath のシンボリックリンクと通常ファイルの規則のみを適用し、
// 包含チェックは意図的にスキップする。
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
		// 欠落または読めない。呼び出し側が未解決のシンボリックリンクを決して
		// 読まないよう拒否する。
		// Missing or unreadable. Reject so the caller never reads an
		// unresolved symlink.
		return "", false
	}
	info, serr := os.Stat(real)
	if serr != nil || !info.Mode().IsRegular() {
		return "", false
	}
	// 許可プレフィックスが設定されていれば、実体がその配下にあることを要求する。
	// 解決後のパスで比較するため、シンボリックリンク経由の脱出も検出できる。
	// If an allowlist is configured, require the real path to be under one of
	// the prefixes. Compare on the resolved path so a symlink escape is caught.
	if len(l.extraAllowedPrefixes) > 0 {
		realClean := filepath.Clean(real)
		ok := false
		for _, pre := range l.extraAllowedPrefixes {
			if pre == "" {
				continue
			}
			rp := pre
			if r, err := filepath.EvalSymlinks(pre); err == nil {
				rp = r
			}
			rp = filepath.Clean(rp)
			if realClean == rp || strings.HasPrefix(realClean, rp+string(os.PathSeparator)) {
				ok = true
				break
			}
		}
		if !ok {
			return "", false
		}
	}
	return filepath.Clean(real), true
}

// handleLogs はログファイルの末尾を返す。
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
	// 過剰なメモリ使用を避けるため行数を制限する。UI は最大 500 行しか保持しない
	// ので、これは十分余裕がある。
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
	// ログ行は攻撃者が制御するテキスト（ユーザー名、パス）を含み得る。
	// text/plain は現代のブラウザに HTML としてスニッフされないが、nosniff で
	// その保証を明示し、ログ行がマークアップとして解釈されることが決してないようにする。
	// Log lines can contain attacker-controlled text (usernames, paths).
	// text/plain is not sniffed as HTML by modern browsers, but nosniff makes
	// that guarantee explicit so a log line can never be interpreted as markup.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(logContent))
}

// handleLogStream は Server-Sent Events でログをストリームする。
// handleLogStream streams logs via Server-Sent Events.
func (l *LogHandler) handleLogStream(w http.ResponseWriter, r *http.Request) {
	logType := r.URL.Query().Get("type")

	logPath, ok := l.resolveLogPath(logType)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "Invalid log type")
		return
	}

	// SSE ヘッダ。
	// SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// ログ行は攻撃者が制御するテキスト（ユーザー名、パス）を含み得る。
	// 非ストリーミングの /api/logs ハンドラは同じ理由で nosniff を設定する。
	// SSE もそうしなければならず、ログ行が別の型としてスニッフされることが
	// 決してないようにする。
	// Log lines can contain attacker-controlled text (usernames, paths). The
	// non-streaming /api/logs handler sets nosniff for the same reason; SSE
	// must too, so a log line can never be sniffed as a different type.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// nginx（および同種のプロキシ）にこの応答をバッファリングしないよう指示する。
	// そうでなければログ行はライブではなくバーストで届く。
	// Tell nginx (and similar proxies) not to buffer this response,
	// otherwise log lines are delivered in bursts instead of live.
	w.Header().Set("X-Accel-Buffering", "no")

	// 既存ログの末尾のみを送る。ファイル全体を吐くと SSE 接続ごとに数メガバイトを
	// 送り、大きなログがクライアントを停止させ得る。新規行は下で末尾から
	// ストリームする。
	// Send only the tail of the existing log. Dumping the whole file would
	// send megabytes on every SSE connection and let a large log stall the
	// client. New lines are streamed from the end below.
	const initialLines = 200
	tail, endPos, err := l.readLastLines(logPath, initialLines)
	if err != nil {
		// エラー本文の改行も SSE の行終端になるため除去する。
		// The error body's newlines are also SSE line terminators, so strip them.
		msg := strings.NewReplacer("\r", " ", "\n", " ").Replace(err.Error())
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", msg)
		return
	}
	for _, line := range strings.Split(tail, "\n") {
		if line == "" {
			continue
		}
		// 素の CR も SSE の行終端なので、ログ行に埋め込まれた '\r' は行を分割し、
		// 偽造の "data:" 行をビューアへ注入し得る。CR を除去し、各ログ行が
		// 1 つの SSE イベントに留まるようにする。
		// A bare CR is an SSE line terminator too, so an embedded '\r'
		// in a log line could split it and inject a forged "data:" line
		// into the viewer. Strip CR so each log line stays one SSE event.
		fmt.Fprintf(w, "data: %s\n\n", strings.ReplaceAll(strings.TrimRight(line, "\r"), "\r", ""))
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	// 末尾がカバーした位置の終端からストリームを開始する。
	// Start streaming from the end of what the tail covered.
	lastPosition := endPos

	// 新規ログを監視する（単純なポーリング）。
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
				// The error body's newlines are also SSE line terminators, so strip them.
				msg := strings.NewReplacer("\r", " ", "\n", " ").Replace(err.Error())
				fmt.Fprintf(w, "event: error\ndata: %s\n\n", msg)
				return
			}
			lastPosition = currentPosition
		}
	}
}

// readLastLines はファイルの最後の N 行を読む。
// ファイル全体をロードするのではなく、ブロック単位で後方へ読む。
// 返される int64 は読み取りがカバーしたファイルサイズ（バイトオフセット）で、
// 呼び出し側は行を欠落・重複させずに正確にそこからストリームを続けられる。
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
			return "", 0, nil // ファイル欠落 -> 空 / missing file -> empty
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
	// メモリへ読むファイル量のハード上限。これが無いと、改行が少ない（または
	// 全く無い）大きなファイルが全体を読まれ、巨大なログがメモリを枯渇させ得る。
	// 4 MiB は要求される行数が必要とする量よりはるかに多い。
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

	// n 個の改行が見つかる（またはバイト上限に達する）まで末尾から読む。
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

// sendFileFromPosition は位置からファイル内容を SSE で送る。
// sendFileFromPosition sends file content from a position via SSE.
func (l *LogHandler) sendFileFromPosition(w http.ResponseWriter, filePath string, position int64) (int64, error) {
	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return position, nil // ファイル欠落 -> 位置を保つ / missing file -> keep position
		}
		return position, err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return position, err
	}

	// ログローテーション/切り詰めを検知する。ファイルが現在の位置より小さければ
	// ローテーションされた。新規行がストリームし続けるよう先頭から再開する。
	// そうしないとストリームが永遠に黙って止まる。
	// Detect log rotation/truncation: if the file is now smaller than our
	// position, it was rotated. Restart from the beginning so new lines keep
	// streaming; otherwise the stream would silently stop forever.
	if stat.Size() < position {
		position = 0
	}

	if stat.Size() <= position {
		return position, nil // 新規ログなし / no new logs
	}

	_, err = file.Seek(position, io.SeekStart)
	if err != nil {
		return position, err
	}

	// 残りを読んで送る。bufio.Reader.ReadString/ReadBytes は CRLF ファイルでも
	// 正確なバイト数を追跡する。Scanner は '\r' を剥がし、位置追跡を狂わせる。
	//
	// リーダバッファは有界だが、1 行はそうではない。ReadBytes は '\n' を見つけるまで
	// 蓄積し続けるため、非常に長い改行なしの区間（暴走プラグインが 1 つの巨大な行を
	// 書く）を持つログは全体がメモリへ読まれ、dashboard を枯渇させ得る。そこで
	// ReadString を行あたりのバイト上限付きで使う。上限に達すると行の残りは破棄され
	// （ストリームは正確なバイト数だけ進むので位置は正しく保たれ、次のポーリングは
	// その行の後から再開する）。
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
	const maxStreamLineBytes = 1 << 20 // 1 行あたり 1 MiB / 1 MiB per line
	reader := bufio.NewReaderSize(file, 64*1024)
	var newPosition int64 = position
	for {
		line, readErr := readLineCapped(reader, maxStreamLineBytes)
		// 完全な行（'\n' で終端）のみを消費する。EOF の部分行は次のポーリングへ
		// 残す。そうしないと、その末尾が 2 回送られる（今と、後で完成した行の
		// 一部として再び）。
		// Only consume complete lines (terminated by '\n'). A partial line at
		// EOF is left for the next poll, otherwise its tail would be sent
		// twice: once now and again as part of the completed line later.
		if readErr == nil && line.complete {
			// 行内の任意の CR を剥がす（素の CR は SSE の行終端で、残すと
			// ログ行が偽造の "data:" イベントをビューアへ注入し得る）。
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

// streamLine はログストリームから読んだ 1 行だ。consumed はリーダから取った正確な
// バイト数（'\n' があれば含む）で、呼び出し側は再読み取りせずに位置を進められる。
// complete は '\n' が行を終端したとき true になる。上限付きで未終端の行は false に
// なり、呼び出し側が部分行を完成行のように出さないようにする。
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

// readLineCapped は r から 1 行を最大 max バイトまで読む。'\n' が見つかる前に
// max を超えた場合、残りは破棄され（次の '\n' または EOF まで排出）、complete=false が
// 返る。バイトは依然 consumed に数えられるため、呼び出し側の位置は正確に保たれ、
// データが再送されることもない。
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
			// それ以上バイトが無い状態での EOF（または読み取りエラー）。
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
		// 上限到達: 次の呼び出しが行境界から始まるよう行の残りを排出し、
		// 破棄したバイトを数える。
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
