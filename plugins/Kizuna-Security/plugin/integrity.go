package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"Kizuna-Eye/pkg/fsutil"
	"Kizuna-Eye/pkg/module"
)

// alertHistoryState is the persisted baseline for the lightweight consistency
// check of alert_history.jsonl (which has no hash chain of its own).
type alertHistoryState struct {
	Lines    int    `json:"lines"`
	FileHash string `json:"file_hash"`
}

// runIntegrityChecks verifies, on every tick:
//   - kizuna-security.log: HMAC hash chain (VerifyChainKeyed).
//   - alert_history.jsonl: line count must not decrease, and a same-length
//     file must keep the same whole-file hash.
//
// The dashboard compacts alert_history.jsonl (drops old entries) as a normal
// operation, so a DECREASE in line count cannot be told apart from a
// truncation attack and is reported as a warning, not critical. A same-length
// in-place modification is a genuine tamper and is reported as critical.
func (p *SecurityPlugin) runIntegrityChecks() {
	p.mu.RLock()
	cfg := p.config
	mon := p.monitor
	p.mu.RUnlock()
	if cfg == nil || mon == nil {
		return
	}

	// --- security.log chain (HMAC) ---
	if cfg.LogPath != "" {
		key := readChainKey(cfg.ChainKeyPath)
		if err := VerifyChainKeyed(cfg.LogPath, key); err != nil {
			p.emitIntegrityAlert(mon, "critical", "log_chain_broken",
				msg(cfg.Language, "integrity.chain.title"),
				msg(cfg.Language, "integrity.chain.msg", cfg.LogPath, err.Error()))
		}
		// F-2: HMAC チェーンは行の改ざんと先頭削除を検知できるが、末尾を
		// 削った場合は残った前方部分が正しく検証できてしまう。行数の最大値を
		// 記録して、末尾削除・古いファイルへの巻き戻しを検知する。
		if reason, level := checkLogMonotonic(cfg.LogPath, logStatePathFor(cfg.LogPath)); reason != "" {
			p.emitIntegrityAlert(mon, level, "log_truncated",
				msg(cfg.Language, "integrity.logtrunc.title"),
				msg(cfg.Language, "integrity.logtrunc.msg", cfg.LogPath, reason))
		}
		if cfg.ChainKeyPath != "" {
			warnChainKeyPermissions(p, mon, cfg.ChainKeyPath, cfg.Language)
		}
	}

	// --- alert_history.jsonl lightweight consistency ---
	if cfg.AlertHistoryPath != "" {
		reason, level := checkAlertHistory(cfg.AlertHistoryPath, cfg.AlertHistoryStatePath)
		if reason != "" {
			p.emitIntegrityAlert(mon, level, "alert_history_tamper",
				msg(cfg.Language, "integrity.alerthist.title"),
				msg(cfg.Language, "integrity.alerthist.msg", cfg.AlertHistoryPath, reason))
		}
	}
}

// emitIntegrityAlert emits an event and logs it.
func (p *SecurityPlugin) emitIntegrityAlert(mon *Monitor, level, event, title, body string) {
	mon.emit(module.SecurityEvent{
		Category:  "integrity",
		Level:     level,
		Title:     title,
		Message:   body,
		Source:    event,
		Timestamp: time.Now(),
	})
	if p.logger != nil {
		if level == "critical" {
			p.logger.Error("Kizuna-Security 整合性検証失敗: %s", body)
		} else {
			p.logger.Warn("Kizuna-Security 整合性検証: %s", body)
		}
	}
}

// readChainKey returns the HMAC key bytes, or nil when unavailable.
func readChainKey(path string) []byte {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return b
}

// checkAlertHistory returns (reason, level). reason is "" when consistent.
//   - line count decreased       -> warning (compaction or truncation)
//   - same lines, different hash -> critical (in-place tamper)
func checkAlertHistory(path, statePath string) (string, string) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ""
		}
		return "open failed: " + err.Error(), "warning"
	}
	defer f.Close()

	h := sha256.New()
	lines := 0
	buf := make([]byte, 64*1024)
	var last byte
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			last = buf[n-1]
			for _, b := range buf[:n] {
				if b == '\n' {
					lines++
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "read failed: " + rerr.Error(), "warning"
		}
	}
	if last != '\n' && last != 0 {
		lines++
	}
	sum := hex.EncodeToString(h.Sum(nil))

	var st alertHistoryState
	if data, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(data, &st)
	}

	reason, level := "", ""
	switch {
	case st.Lines > 0 && lines < st.Lines:
		reason = fmt.Sprintf("line count decreased (%d -> %d): possible compaction or truncation", st.Lines, lines)
		level = "warning"
	case st.Lines > 0 && lines == st.Lines && st.FileHash != "" && sum != st.FileHash:
		reason = "file modified in place (same line count, different hash)"
		level = "critical"
	}

	// Persist the new baseline (on append, compaction, or first run).
	newState := alertHistoryState{Lines: lines, FileHash: sum}
	if newState.Lines != st.Lines || newState.FileHash != st.FileHash {
		if data, err := json.Marshal(newState); err == nil {
			// fsutil: 同一ディレクトリに一時ファイル→fsync→rename。従来の
			// ".tmp 固定名 + fsync 無し" はクラッシュで切り詰められ得た。
			_ = fsutil.WriteFileAtomic(statePath, data, 0600)
		}
	}
	return reason, level
}

// logLinesState はセキュリティログの行数の最大値（単調増加のアンカー）。
type logLinesState struct {
	Lines int `json:"lines"`
}

// logStatePathFor は行数アンカーの保存先を返す（ログと同じディレクトリ）。
func logStatePathFor(logPath string) string {
	return dirOf(logPath) + "/kizuna-security-logstate.json"
}

// checkLogMonotonic はセキュリティログの行数が減っていないかを確認する。
// 戻り値は (reason, level)。reason が空なら問題なし。
//
// HMAC チェーンは「行の改ざん」と「先頭の削除」を検知できるが、末尾を削ると
// 残った前方部分が正しく検証できてしまう（F-2 の残存リスク）。行数の最大値を
// 記録しておくことで、末尾削除・ログ消失・古いファイルへの巻き戻しを
// critical として検知する。
func checkLogMonotonic(path, statePath string) (string, string) {
	var st logLinesState
	if data, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(data, &st)
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			if st.Lines > 0 {
				return fmt.Sprintf("security log disappeared (was %d lines)", st.Lines), "critical"
			}
			return "", ""
		}
		return "open failed: " + err.Error(), "warning"
	}
	defer f.Close()

	lines := 0
	var last byte
	buf := make([]byte, 64*1024)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			last = buf[n-1]
			for _, b := range buf[:n] {
				if b == '\n' {
					lines++
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "read failed: " + rerr.Error(), "warning"
		}
	}
	if last != '\n' && last != 0 {
		lines++
	}

	reason, level := "", ""
	if st.Lines > 0 && lines < st.Lines {
		reason = fmt.Sprintf("line count decreased (%d -> %d): truncated or rolled back", st.Lines, lines)
		level = "critical"
	}
	if lines > st.Lines {
		if data, err := json.Marshal(logLinesState{Lines: lines}); err == nil {
			_ = fsutil.WriteFileAtomic(statePath, data, 0600)
		}
	}
	return reason, level
}

// chainKeyPermWarnInterval is how long the same permission problem stays quiet
// before it is reported again. Medium-8: the previous implementation used
// sync.Once, so the warning fired at most once per process lifetime — an
// operator who saw it, "fixed" it, and later regressed the mode (or restored a
// backup) never got a second warning. Tests override this value.
var chainKeyPermWarnInterval = 6 * time.Hour

// permWarnState rate-limits the permission warning per key path while still
// warning again when the mode changes or after the interval elapses.
type permWarnState struct {
	mu       sync.Mutex
	lastPerm map[string]string
	lastAt   map[string]time.Time
}

var chainKeyPermWarner = &permWarnState{
	lastPerm: map[string]string{},
	lastAt:   map[string]time.Time{},
}

// shouldWarn reports whether the warning for keyPath/perm must be emitted now,
// and records the emission.
func (w *permWarnState) shouldWarn(keyPath, perm string, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lastPerm[keyPath] == perm {
		if t, ok := w.lastAt[keyPath]; ok && now.Sub(t) < chainKeyPermWarnInterval {
			return false
		}
	}
	w.lastPerm[keyPath] = perm
	w.lastAt[keyPath] = now
	return true
}

// forget drops the record for keyPath, so that fixing the mode and then
// regressing it is reported immediately instead of waiting for the interval.
func (w *permWarnState) forget(keyPath string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.lastPerm, keyPath)
	delete(w.lastAt, keyPath)
}

// warnChainKeyPermissions はチェーン鍵が他ユーザーから読める場合に警告する
// （F-2）。鍵が読めると、攻撃者はログを自由に再署名できてチェーン検証を
// 無効化できる。鍵は agent 専用ユーザーのみ読める 0600 にすべきで、
// 共有フォルダ（Samba 共有）に置くべきではない。
func warnChainKeyPermissions(p *SecurityPlugin, mon *Monitor, keyPath, lang string) {
	info, err := os.Stat(keyPath)
	if err != nil {
		return
	}
	perm := info.Mode().Perm()
	if perm&0o077 == 0 {
		// Fixed (or never broken): forget the record so a regression warns
		// immediately.
		chainKeyPermWarner.forget(keyPath)
		return
	}
	permStr := fmt.Sprintf("%04o", perm)
	if !chainKeyPermWarner.shouldWarn(keyPath, permStr, time.Now()) {
		return
	}
	p.emitIntegrityAlert(mon, "warning", "chain_key_permissions",
		msg(lang, "integrity.keyperm.title"),
		msg(lang, "integrity.keyperm.msg", keyPath, permStr))
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			if i == 0 {
				return "/"
			}
			return p[:i]
		}
	}
	return "."
}
