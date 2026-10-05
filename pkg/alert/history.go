package alert

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"Kizuna-Eye/pkg/fsutil"
	"Kizuna-Eye/pkg/notify"
)

// HistoryEntry is one alert history record (for API output).
type HistoryEntry struct {
	Type      string    `json:"type"`
	Level     string    `json:"level"`
	Icon      string    `json:"icon"`
	Title     string    `json:"title"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
	// Source identifies where the alert came from ("dashboard", "agent", ...).
	Source string `json:"source,omitempty"`
	// Trusted is true for alerts the dashboard generated itself (not derived
	// from a log line an attacker could forge). Untrusted alerts are evicted
	// first when the history is full, so a flood of forged alerts cannot push
	// genuine ones out.
	Trusted bool `json:"trusted,omitempty"`

	// --- アラート詳細表示用 (タスク1) ---
	// ID is a stable identifier derived from Timestamp+Type+Title, used by
	// the detail API and the one-click action API. It is not persisted (it is
	// recomputed on load), so the format may change between versions.
	ID string `json:"id,omitempty"`
	// Command / DetectFile / DetectLine / Remediation / RelatedLog are copied
	// from the originating SecurityEvent. Older records predate these fields
	// and simply omit them.
	Command     string `json:"command,omitempty"`
	DetectFile  string `json:"detect_file,omitempty"`
	DetectLine  int    `json:"detect_line,omitempty"`
	Remediation string `json:"remediation,omitempty"`
	RelatedLog  string `json:"related_log,omitempty"`

	// TitleEN / MessageEN carry the English rendering of the alert so the UI
	// can switch the alert history between languages without re-deriving the
	// string from a key (task 9). Older records omit them.
	TitleEN   string `json:"title_en,omitempty"`
	MessageEN string `json:"message_en,omitempty"`

	// Sig is the detached HMAC-SHA256 of the entry (with Sig removed) when
	// alert-history signing is enabled. Legacy lines omit it and are skipped
	// by the verifier, so enabling signing never invalidates old history.
	Sig string `json:"sig,omitempty"`
}

// maxPersistBytes is the size at which the persistence file is rewritten
// with only the in-memory entries.
const maxPersistBytes = 4 << 20 // 4 MiB

// levelWeight returns the retention weight for a level. Higher is kept longer.
func levelWeight(level string) int {
	switch strings.ToLower(level) {
	case "critical":
		return 3
	case "warning":
		return 2
	default: // info, success, unknown
		return 1
	}
}

// History is a ring buffer of alert history, optionally persisted.
type History struct {
	mu      sync.RWMutex
	entries []HistoryEntry
	max     int
	path    string
	// sigKey is the HMAC key used to sign persisted lines. Empty disables
	// signing (lines are written unsigned; fully backward compatible).
	sigKey []byte
	logf   func(format string, args ...interface{})
}

// SetSigningKey enables HMAC signing of persisted lines. A nil/empty key
// disables signing. Call before SetPersistence.
func (h *History) SetSigningKey(key []byte) {
	h.mu.Lock()
	h.sigKey = key
	h.mu.Unlock()
}

func (h *History) SetLogger(logf func(format string, args ...interface{})) {
	h.mu.Lock()
	h.logf = logf
	h.mu.Unlock()
}

// NewHistory creates a History with the given capacity. max <= 0 defaults to 200.
func NewHistory(max int) *History {
	if max <= 0 {
		max = 200
	}
	return &History{
		entries: make([]HistoryEntry, 0, max),
		max:     max,
	}
}

// SetPersistence enables appending records to path (JSON Lines).
func (h *History) SetPersistence(path string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.path = path
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	// 0600 へ締めるが、A-4 の移行が与えた group read (0640) は残す。このファイルは
	// ダッシュボードが書き、agent (kizuna-agent) が V2-B の整合性検証で読むため、
	// 無条件の 0600 は agent に「open failed」の警告を出させ続ける。
	fsutil.TightenSharedConfigMode(path)
}

// Add appends one record with no source information.
func (h *History) Add(a *notify.Alert) {
	h.AddSourced(a, "", false)
}

// AddSourced appends one record with its source, dropping the oldest
// low-priority entry first when the history is full.
func (h *History) AddSourced(a *notify.Alert, source string, trusted bool) {
	h.AddSourcedDetail(a, source, trusted, HistoryEntry{})
}

// AddSourcedDetail is like AddSourced but also carries the optional detail
// fields (Command/DetectFile/DetectLine/Remediation/RelatedLog) that the
// security plugin attaches to its events (task 1: alert detail view).
// Zero-valued fields are treated as "not recorded".
func (h *History) AddSourcedDetail(a *notify.Alert, source string, trusted bool, detail HistoryEntry) {
	if a == nil {
		return
	}
	h.mu.RLock()
	fn := h.logf
	h.mu.RUnlock()
	logf := func(format string, args ...interface{}) {
		if fn != nil {
			fn(format, args...)
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	e := HistoryEntry{
		Type:        a.Type,
		Level:       string(a.Level),
		Icon:        a.Icon,
		Title:       a.Title,
		Message:     a.Message,
		Timestamp:   a.Timestamp,
		Source:      source,
		Trusted:     trusted,
		Command:     detail.Command,
		DetectFile:  detail.DetectFile,
		DetectLine:  detail.DetectLine,
		Remediation: detail.Remediation,
		RelatedLog:  detail.RelatedLog,
		TitleEN:     detail.TitleEN,
		MessageEN:   detail.MessageEN,
	}
	e.ID = entryID(e)
	h.entries = append(h.entries, e)
	h.trimLocked()

	if h.path != "" {
		// 新規作成時は fsutil.SharedFileMode（既存ファイルの group read 保持、
		// 共有ディレクトリでの再作成は 0640）を O_CREATE のモードに使う。
		f, err := openAppendNoFollow(h.path, fsutil.SharedFileMode(h.path, 0600))
		if err != nil {
			logf("アラート履歴の永続化: open に失敗 (%s): %v", h.path, err)
		} else {
			if sig, serr := signHistoryEntry(h.sigKey, e); serr == nil && sig != "" {
				e.Sig = sig
			}
			b, merr := json.Marshal(e)
			if merr != nil {
				logf("アラート履歴の永続化: JSON化に失敗: %v", merr)
			} else if _, werr := f.Write(append(b, '\n')); werr != nil {
				logf("アラート履歴の永続化: 書き込みに失敗: %v", werr)
			}
			_ = f.Close()
		}
		if info, err := os.Stat(h.path); err == nil && info.Size() > maxPersistBytes {
			h.compactLocked()
		}
	}
}

// entryID returns a stable short identifier for a history entry. It is
// derived from fields that do not change once the entry is created, so it
// survives a reload of the persistence file. Two alerts that share the same
// timestamp, type and title (a rare but possible collision in a burst) share
// an ID; the detail/action APIs therefore locate the first match and treat
// the ID as a locator, not a unique key.
func entryID(e HistoryEntry) string {
	seed := e.Timestamp.UTC().Format(time.RFC3339Nano) + "|" + e.Type + "|" + e.Title
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:8])
}

// dedupKey identifies a repeated alert that should be collapsed to its
// newest instance when the history is under pressure. It combines the
// fields that stay the same across a burst of the same fact: type, source,
// title and the leading part of the message (the target — a file path, a
// port, a username).
//
// Without this, a single attacker (or an over-eager detector) can flood
// the history with structurally identical entries — e.g. the same /tmp file
// changing over and over, or agent_disconnect firing in a loop — and evict
// unrelated, more important alerts (a new listening port, a cron change)
// simply because the flood has a higher level weight. Collapsing repeats
// first keeps a representative of each distinct fact visible regardless of
// how noisy one source becomes.
func dedupKey(e HistoryEntry) string {
	m := e.Message
	// Cap the message prefix so the key stays small but still identifies
	// the target. Distinguishing e.g. "/tmp/a" from "/tmp/b" only needs the
	// first path token, which always appears before the first space.
	if len(m) > 80 {
		m = m[:80]
	}
	return e.Type + "\x00" + e.Source + "\x00" + e.Title + "\x00" + m
}

// trimLocked drops entries until the length is within max. Two stages:
//
//  1. Collapse exact repeats (same dedupKey) to the newest instance. This
//     keeps a burst from one source (a file changing again and again, a
//     reconnect loop) from filling the buffer and evicting unrelated alerts.
//  2. If still over capacity, remove the least important oldest entries:
//     an entry is a removal candidate by (trusted, level weight), lowest
//     first, then oldest. So critical alerts outlive info/success, and
//     trusted alerts outlive untrusted ones.
//
// Caller must hold h.mu.
func (h *History) trimLocked() {
	if len(h.entries) <= h.max {
		return
	}

	// Stage 1: collapse repeats, keeping the newest of each dedupKey.
	newest := make(map[string]int, len(h.entries))
	for i, e := range h.entries {
		newest[dedupKey(e)] = i
	}
	dedup := h.entries[:0]
	for i, e := range h.entries {
		if newest[dedupKey(e)] == i {
			dedup = append(dedup, e)
		}
	}
	h.entries = dedup

	if len(h.entries) <= h.max {
		return
	}

	// Stage 2: cap how much any single Type may occupy. A noisy detector
	// (e.g. FIM watching a busy /tmp, or an agent reconnect loop) emits
	// hundreds of structurally distinct entries of one Type; collapsing
	// exact repeats (stage 1) does not help because the message/source
	// differs each time. Without a per-type cap those entries evict
	// everything else — including a single important alert of another Type
	// — once the buffer is full. Keep at most half of max per Type (newest
	// first) and let stage 3 handle the rest.
	//
	// The cap only applies when more than one Type is present: if the
	// history holds a single Type there is nothing else to protect, and
	// capping would throw away entries unnecessarily (the ring buffer is
	// the only limit the caller asked for).
	if h.max >= 4 {
		types := make(map[string]struct{}, 8)
		for _, e := range h.entries {
			types[e.Type] = struct{}{}
		}
		if len(types) > 1 {
			capPerType := h.max / 2
			if capPerType < 2 {
				capPerType = 2
			}
			counts := make(map[string]int, 8)
			capped := h.entries[:0]
			// Walk newest -> oldest so the newest entries of each Type are kept.
			for i := len(h.entries) - 1; i >= 0; i-- {
				e := h.entries[i]
				if counts[e.Type] >= capPerType {
					continue
				}
				counts[e.Type]++
				capped = append(capped, e)
			}
			// capped is newest-first; restore oldest-first order for the rest of
			// the trimming (which assumes chronological order).
			for i, j := 0, len(capped)-1; i < j; i, j = i+1, j-1 {
				capped[i], capped[j] = capped[j], capped[i]
			}
			h.entries = capped
			if len(h.entries) <= h.max {
				return
			}
		}
	}

	// Stage 3: still over capacity, so evict by (trusted, weight, age).
	need := len(h.entries) - h.max

	// Build an index list and sort by eviction priority (the first `need`
	// entries are removed). Lower rank = removed first.
	type ranked struct {
		i     int
		trust int
		w     int
		ts    time.Time
	}
	rs := make([]ranked, len(h.entries))
	for i, e := range h.entries {
		trust := 0
		if e.Trusted {
			trust = 1
		}
		rs[i] = ranked{i: i, trust: trust, w: levelWeight(e.Level), ts: e.Timestamp}
	}
	sort.SliceStable(rs, func(a, b int) bool {
		x, y := rs[a], rs[b]
		if x.trust != y.trust {
			return x.trust < y.trust // untrusted removed first
		}
		if x.w != y.w {
			return x.w < y.w // lower level removed first
		}
		return x.ts.Before(y.ts) // oldest removed first
	})
	remove := make(map[int]bool, need)
	for k := 0; k < need; k++ {
		remove[rs[k].i] = true
	}
	kept := h.entries[:0]
	for i, e := range h.entries {
		if !remove[i] {
			kept = append(kept, e)
		}
	}
	h.entries = kept
}

// compactLocked rewrites the persistence file with only the current entries.
func (h *History) compactLocked() {
	if h.path == "" {
		return
	}
	var out []byte
	for _, e := range h.entries {
		if sig, serr := signHistoryEntry(h.sigKey, e); serr == nil && sig != "" {
			e.Sig = sig
		}
		b, err := json.Marshal(e)
		if err != nil {
			continue
		}
		out = append(out, b...)
		out = append(out, '\n')
	}
	// fsutil.WriteFileAtomic: 同一ディレクトリの一時ファイルに書き、fsync して
	// から rename する。同じ処理が10箇所に散っていたので共通化した（L-12）。
	// モードは rename 前に決まるため、group read の保持は fsutil.SharedFileMode で
	// 先に解決する（A-4: agent が読むファイルを 0600 へ戻さない）。
	if err := fsutil.WriteFileAtomic(h.path, out, fsutil.SharedFileMode(h.path, 0600)); err != nil {
		return
	}
}

// Load reads persisted records from path (newest kept up to max).
func (h *History) Load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var all []HistoryEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e HistoryEntry
		if err := json.Unmarshal(line, &e); err == nil {
			if e.ID == "" {
				e.ID = entryID(e)
			}
			all = append(all, e)
		}
	}
	scanErr := sc.Err()
	_ = f.Close()

	h.mu.Lock()
	defer h.mu.Unlock()
	h.path = path
	h.entries = all
	trimmed := len(h.entries) > h.max
	h.trimLocked()
	if trimmed {
		h.compactLocked()
	}
	return scanErr
}

// List returns a copy of the history, newest first.
func (h *History) List() []HistoryEntry {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]HistoryEntry, len(h.entries))
	for i, e := range h.entries {
		out[len(h.entries)-1-i] = e
	}
	return out
}

// Len returns the number of records.
func (h *History) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.entries)
}

// Clear removes every record from memory and archives the persistence file.
func (h *History) Clear() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.entries = make([]HistoryEntry, 0, h.max)

	if h.path == "" {
		return nil
	}
	if _, err := os.Stat(h.path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	archive := fmt.Sprintf("%s.%s.archive", h.path, time.Now().UTC().Format("20060102T150405Z"))
	if err := os.Rename(h.path, archive); err != nil {
		if fnerr := os.Truncate(h.path, 0); fnerr != nil && !os.IsNotExist(fnerr) {
			return fnerr
		}
		return nil
	}
	_ = os.Chmod(archive, 0600)
	h.pruneArchivesLocked()
	return nil
}

// maxArchives is how many archived alert-history files are kept after a clear.
const maxArchives = 10

// pruneArchivesLocked removes the oldest <path>.*.archive files.
func (h *History) pruneArchivesLocked() {
	if h.path == "" {
		return
	}
	dir := filepath.Dir(h.path)
	base := filepath.Base(h.path) + "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var archives []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, base) && strings.HasSuffix(name, ".archive") {
			archives = append(archives, name)
		}
	}
	if len(archives) <= maxArchives {
		return
	}
	sort.Strings(archives) // timestamp suffix sorts chronologically
	for _, name := range archives[:len(archives)-maxArchives] {
		_ = os.Remove(filepath.Join(dir, name))
	}
}
