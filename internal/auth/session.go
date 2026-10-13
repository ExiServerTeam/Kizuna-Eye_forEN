package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// maxSessions bounds the total number of in-memory sessions. Without a cap, an
// unauthenticated client could call the guest-login endpoint in a loop and grow
// the session map without bound (memory-exhaustion DoS).
const maxSessions = 4096

// ErrTooManySessions is returned when a new session cannot be created because
// the store is full and no session could be evicted.
var ErrTooManySessions = errors.New("セッション数が上限に達しました")

// Session is one authenticated browser session.
type Session struct {
	// ID is the raw bearer token. It is delivered only in the cookie and is
	// NEVER written to disk (json:"-"). The in-memory map and the persisted
	// file are keyed by sha256(ID) instead, so a leaked session file cannot be
	// used to hijack sessions.
	ID        string    `json:"-"`
	Username  string    `json:"username"`
	Role      Role      `json:"role"`
	IP        string    `json:"ip,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen"`
	ExpiresAt time.Time `json:"expires_at"`
}

// SessionManager stores sessions. Sessions are keyed in memory and on disk by
// sha256(token); the raw token lives only in the browser cookie.
type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*Session // key = sha256 hex of the raw token
	ttl      time.Duration
	idle     time.Duration
	max      int

	// path is the persistence file. Empty disables persistence (in-memory
	// only, sessions lost on restart).
	path string
	// ipBind binds a session to the IP that created it (optional).
	ipBind bool

	stopCh chan struct{}
}

// NewSessionManager creates a SessionManager.
// ttl is the absolute lifetime; idle is the inactivity timeout.
func NewSessionManager(ttl, idle time.Duration) *SessionManager {
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	if idle <= 0 {
		idle = 2 * time.Hour
	}
	m := &SessionManager{
		sessions: make(map[string]*Session),
		ttl:      ttl,
		idle:     idle,
		max:      maxSessions,
		stopCh:   make(chan struct{}),
	}
	go m.reaper()
	return m
}

// EnablePersistence turns on session persistence at path and (optionally) IP
// binding. It loads any existing sessions first. An empty path keeps the
// manager in-memory only. Call once during startup before serving requests.
func (m *SessionManager) EnablePersistence(path string, ipBind bool) error {
	m.mu.Lock()
	m.path = path
	m.ipBind = ipBind
	m.mu.Unlock()
	if path == "" {
		return nil
	}
	return m.load()
}

// IPBindEnabled reports whether sessions are bound to their client IP.
func (m *SessionManager) IPBindEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.ipBind
}

// hashToken returns the hex sha256 of a raw session token.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// newID returns a random 32-byte hex session token.
func newID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Create issues a new session for the user. ip may be empty when IP binding is
// not used.
func (m *SessionManager) Create(username string, role Role, ip string) (*Session, error) {
	token, err := newID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	s := &Session{
		ID:        token,
		Username:  username,
		Role:      role,
		IP:        ip,
		CreatedAt: now,
		LastSeen:  now,
		ExpiresAt: now.Add(m.ttl),
	}
	key := hashToken(token)

	m.mu.Lock()
	// Bound the store so an unauthenticated guest-login flood cannot exhaust
	// memory. Evict the least-recently-used session (guest sessions first).
	if m.max > 0 && len(m.sessions) >= m.max {
		if !m.evictLocked() {
			m.mu.Unlock()
			return nil, ErrTooManySessions
		}
	}
	m.sessions[key] = s
	m.mu.Unlock()

	m.save()
	return s, nil
}

// evictLocked removes one session to make room. It prefers an idle guest
// session, then falls back to the least-recently-seen session. Caller must hold
// m.mu. Returns false only when there is nothing to evict.
func (m *SessionManager) evictLocked() bool {
	var guestKey, oldestKey string
	var guestSeen, oldestSeen time.Time
	first := true
	for key, s := range m.sessions {
		if s.Username == "guest" && (guestKey == "" || s.LastSeen.Before(guestSeen)) {
			guestKey = key
			guestSeen = s.LastSeen
		}
		if first || s.LastSeen.Before(oldestSeen) {
			oldestKey = key
			oldestSeen = s.LastSeen
			first = false
		}
	}
	if guestKey != "" {
		delete(m.sessions, guestKey)
		return true
	}
	if oldestKey != "" {
		delete(m.sessions, oldestKey)
		return true
	}
	return false
}

// Get returns a session if it is valid. It refreshes the idle timer.
func (m *SessionManager) Get(token string) (*Session, bool) {
	if token == "" {
		return nil, false
	}
	key := hashToken(token)
	now := time.Now()
	m.mu.Lock()
	s, ok := m.sessions[key]
	if !ok {
		m.mu.Unlock()
		return nil, false
	}
	if now.After(s.ExpiresAt) || now.Sub(s.LastSeen) > m.idle {
		delete(m.sessions, key)
		m.mu.Unlock()
		m.save()
		return nil, false
	}
	s.LastSeen = now
	copy := *s
	m.mu.Unlock()
	return &copy, true
}

// Delete removes a session (logout).
func (m *SessionManager) Delete(token string) {
	key := hashToken(token)
	m.mu.Lock()
	delete(m.sessions, key)
	m.mu.Unlock()
	m.save()
}

// DeleteUserSessions removes every session for a username.
// Used when a user is deleted or has their password/role changed.
func (m *SessionManager) DeleteUserSessions(username string) {
	m.mu.Lock()
	changed := false
	for key, s := range m.sessions {
		if s.Username == username {
			delete(m.sessions, key)
			changed = true
		}
	}
	m.mu.Unlock()
	if changed {
		m.save()
	}
}

// DeleteAll removes every session (e.g. after a role/password change on self).
func (m *SessionManager) DeleteAll() {
	m.mu.Lock()
	m.sessions = make(map[string]*Session)
	m.mu.Unlock()
	m.save()
}

// persistedSession is one on-disk record. The raw token is absent; Hash is the
// sha256 of the token so lookups after restart still work.
type persistedSession struct {
	Hash      string    `json:"hash"`
	Username  string    `json:"username"`
	Role      Role      `json:"role"`
	IP        string    `json:"ip,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen"`
	ExpiresAt time.Time `json:"expires_at"`
}

// load reads the persistence file (if present). Missing file is not an error.
func (m *SessionManager) load() error {
	m.mu.RLock()
	path := m.path
	m.mu.RUnlock()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	// The file holds session hashes; keep it owner-only.
	_ = os.Chmod(path, 0600)

	var file struct {
		Sessions []persistedSession `json:"sessions"`
	}
	if len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return err
	}

	now := time.Now()
	loaded := make(map[string]*Session, len(file.Sessions))
	for _, rec := range file.Sessions {
		if rec.Hash == "" || rec.Username == "" {
			continue
		}
		// Drop already-expired records so a stale file cannot resurrect them.
		if now.After(rec.ExpiresAt) || now.Sub(rec.LastSeen) > m.idle {
			continue
		}
		loaded[rec.Hash] = &Session{
			Username:  rec.Username,
			Role:      rec.Role,
			IP:        rec.IP,
			CreatedAt: rec.CreatedAt,
			LastSeen:  rec.LastSeen,
			ExpiresAt: rec.ExpiresAt,
		}
	}

	m.mu.Lock()
	m.sessions = loaded
	m.mu.Unlock()
	return nil
}

// save writes the session table atomically. It is a no-op when persistence is
// disabled. Callers must NOT hold m.mu.
func (m *SessionManager) save() {
	m.mu.RLock()
	path := m.path
	list := make([]persistedSession, 0, len(m.sessions))
	for hash, s := range m.sessions {
		list = append(list, persistedSession{
			Hash:      hash,
			Username:  s.Username,
			Role:      s.Role,
			IP:        s.IP,
			CreatedAt: s.CreatedAt,
			LastSeen:  s.LastSeen,
			ExpiresAt: s.ExpiresAt,
		})
	}
	m.mu.RUnlock()
	if path == "" {
		return
	}

	data, err := json.MarshalIndent(map[string]interface{}{"sessions": list}, "", "  ")
	if err != nil {
		return
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return
	}
	if _, err := tmp.Write(data); err != nil {
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
	_ = os.Rename(tmpName, path)
}

// reaper periodically removes expired sessions and flushes LastSeen updates to
// disk so idle timeouts survive a restart.
func (m *SessionManager) reaper() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			now := time.Now()
			m.mu.Lock()
			changed := false
			for key, s := range m.sessions {
				if now.After(s.ExpiresAt) || now.Sub(s.LastSeen) > m.idle {
					delete(m.sessions, key)
					changed = true
				}
			}
			m.mu.Unlock()
			// Persist at least periodically so LastSeen advances on disk.
			_ = changed
			m.save()
		}
	}
}

// Stop terminates the reaper goroutine and flushes sessions to disk.
func (m *SessionManager) Stop() {
	select {
	case <-m.stopCh:
	default:
		close(m.stopCh)
	}
	m.save()
}
