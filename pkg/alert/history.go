package alert

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

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
	logf    func(format string, args ...interface{})
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
	if _, err := os.Stat(path); err == nil {
		_ = os.Chmod(path, 0600)
	}
}

// Add appends one record with no source information.
func (h *History) Add(a *notify.Alert) {
	h.AddSourced(a, "", false)
}

// AddSourced appends one record with its source, dropping the oldest
// low-priority entry first when the history is full.
func (h *History) AddSourced(a *notify.Alert, source string, trusted bool) {
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
		Type:      a.Type,
		Level:     string(a.Level),
		Icon:      a.Icon,
		Title:     a.Title,
		Message:   a.Message,
		Timestamp: a.Timestamp,
		Source:    source,
		Trusted:   trusted,
	}
	h.entries = append(h.entries, e)
	h.trimLocked()

	if h.path != "" {
		f, err := openAppendNoFollow(h.path)
		if err != nil {
			logf("アラート履歴の永続化: open に失敗 (%s): %v", h.path, err)
		} else {
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

// trimLocked drops entries until the length is within max. It removes the
// least important oldest entries first: an entry is a removal candidate by
// (trusted, level weight), lowest first, then oldest. Concretely, it keeps
// critical alerts longer than info/success, and trusted alerts longer than
// untrusted ones, so a flood of forged (untrusted, low-level) alerts cannot
// push genuine high-severity alerts out of the history.
// Caller must hold h.mu.
func (h *History) trimLocked() {
	if len(h.entries) <= h.max {
		return
	}
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
	dir := filepath.Dir(h.path)
	tmp, err := os.CreateTemp(dir, filepath.Base(h.path)+".tmp-*")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	_ = tmp.Chmod(0600)

	w := bufio.NewWriter(tmp)
	for _, e := range h.entries {
		b, err := json.Marshal(e)
		if err != nil {
			continue
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			tmp.Close()
			return
		}
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(tmpName, h.path)
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
