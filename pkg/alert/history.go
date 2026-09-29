package alert

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
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
}

// maxPersistBytes is the size at which the persistence file is rewritten
// with only the in-memory entries. Without this the JSON Lines file would
// grow without bound (every alert is appended, never removed).
const maxPersistBytes = 4 << 20 // 4 MiB

// History is a fixed-size ring buffer of alert history,
// optionally persisted to a JSON Lines file.
type History struct {
	mu      sync.RWMutex
	entries []HistoryEntry
	max     int
	path    string // persistence file (empty = memory only)
	// logf, when set, receives persistence-failure messages so a full disk or
	// permission error does not silently stop recording alerts to disk.
	logf func(format string, args ...interface{})
}

// SetLogger installs a logging function used to report persistence errors.
func (h *History) SetLogger(logf func(format string, args ...interface{})) {
	h.mu.Lock()
	h.logf = logf
	h.mu.Unlock()
}

// NewHistory creates a History with the given capacity.
// max <= 0 defaults to 200.
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
	// Tighten an existing file that may have been created world-readable.
	if _, err := os.Stat(path); err == nil {
		_ = os.Chmod(path, 0600)
	}
}

// Add appends one record, dropping the oldest, and persists it.
func (h *History) Add(a *notify.Alert) {
	if a == nil {
		return
	}
	// Capture the logger before taking the write lock: log() takes a read
	// lock, and RLock while holding Lock on the same RWMutex would deadlock.
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
	}
	h.entries = append(h.entries, e)

	if len(h.entries) > h.max {
		h.entries = h.entries[len(h.entries)-h.max:]
	}

	if h.path != "" {
		// Alert history can contain usernames and IPs; keep it owner-only.
		f, err := os.OpenFile(h.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
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
		// Keep the file bounded: rewrite it with just the in-memory entries
		// once it grows past the threshold.
		if info, err := os.Stat(h.path); err == nil && info.Size() > maxPersistBytes {
			h.compactLocked()
		}
	}
}

// compactLocked rewrites the persistence file with only the current entries.
// Caller must hold h.mu.
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
	// Close before compacting: on Windows, os.Rename cannot replace an open
	// file.
	_ = f.Close()

	h.mu.Lock()
	defer h.mu.Unlock()
	// Record the path so compaction can rewrite this file. The engine may
	// call Load before SetPersistence, so set it here too.
	h.path = path
	trimmed := false
	if len(all) > h.max {
		all = all[len(all)-h.max:]
		trimmed = true
	}
	h.entries = all
	// Compact the on-disk file after loading a larger history, so a bloated
	// file from an older run does not stay bloated forever.
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

// Clear removes every record from memory and truncates the persistence file.
// It returns an error only when the persistence file exists but cannot be
// truncated; a missing file is treated as already empty.
func (h *History) Clear() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.entries = make([]HistoryEntry, 0, h.max)

	if h.path == "" {
		return nil
	}
	if err := os.Truncate(h.path, 0); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
