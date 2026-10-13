package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	"Kizuna-Eye/pkg/fsutil"
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
	// stopOnce makes Stop idempotent and safe under concurrent calls: the
	// select/close pattern it replaced could close stopCh twice and panic.
	stopOnce sync.Once
	// logf, when set, receives persistence-failure messages. Without it a
	// full disk or permission error would silently stop persisting sessions.
	logf func(format string, args ...interface{})

	// saveMu serializes disk writes. save() snapshots the session map under
	// m.mu, releases it, then writes the file; without saveMu two concurrent
	// saves could interleave so that the later-started one finishes first and
	// the older snapshot's rename wins, dropping the newer sessions from disk
	// (lost update). Holding saveMu across snapshot+write makes each save
	// write the newest state and keeps writes ordered.
	saveMu sync.Mutex

	stopCh chan struct{}
}

// SetLogger installs a logging function used to report persistence errors.
func (m *SessionManager) SetLogger(logf func(format string, args ...interface{})) {
	m.mu.Lock()
	m.logf = logf
	m.mu.Unlock()
}

// log persists a message through the optional logger.
func (m *SessionManager) log(format string, args ...interface{}) {
	m.mu.RLock()
	fn := m.logf
	m.mu.RUnlock()
	if fn != nil {
		fn(format, args...)
	}
}

// NewSessionManager creates a SessionManager.
// ttl is the absolute lifetime; idle is the inactivity timeout.
func NewSessionManager(ttl, idle time.Duration) *SessionManager {
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	// idle == 0 selects the default (2h). A negative value disables the idle
	// timeout entirely, so a session stays valid until its absolute TTL. This
	// lets an operator stop the "logged out when I come back" behaviour
	// without losing the absolute expiry as a safety net.
	if idle == 0 {
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
	return m.CreateWithTTL(username, role, ip, m.ttl)
}

// CreateWithTTL is Create with an explicit absolute lifetime. It is used for
// guest sessions, which should expire far sooner than a real user's session:
// a guest is an anonymous, throw-away viewer, so leaving it valid for the full
// (e.g. 30-day) user TTL would keep useless sessions in the store and on disk.
// ttl <= 0 falls back to the manager's configured TTL.
func (m *SessionManager) CreateWithTTL(username string, role Role, ip string, ttl time.Duration) (*Session, error) {
	if ttl <= 0 {
		ttl = m.ttl
	}
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
		ExpiresAt: now.Add(ttl),
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
	if now.After(s.ExpiresAt) || (m.idle > 0 && now.Sub(s.LastSeen) > m.idle) {
		delete(m.sessions, key)
		m.mu.Unlock()
		m.save()
		return nil, false
	}
	s.LastSeen = now
	copy := *s
	// Restore the raw token on the returned copy. Sessions loaded from disk
	// are keyed by sha256(token) and their ID field is empty, so without this
	// a caller doing Delete(session.ID) after a restart would call
	// Delete("") and fail to remove the server-side session (logout and the
	// login-time session-fixation defense would silently do nothing).
	copy.ID = token
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
		if now.After(rec.ExpiresAt) || (m.idle > 0 && now.Sub(rec.LastSeen) > m.idle) {
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
	// Serialize writers so a slow save cannot overwrite a newer one (lost
	// update). saveMu is released before any logger call, which only takes
	// m.mu, so there is no lock-order inversion.
	m.saveMu.Lock()
	defer m.saveMu.Unlock()

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
		m.log("セッション永続化: JSON化に失敗: %v", err)
		return
	}
	data = append(data, '\n')

	// fsutil.WriteFileAtomic: 親ディレクトリを 0700 で用意し、0600 の一時
	// ファイルに fsync してから rename する（L-12: ここにも同じ処理が
	// 重複していた）。
	if err := fsutil.WriteFileAtomic(path, data, 0600); err != nil {
		m.log("セッション永続化: 書き込みに失敗 (%s): %v", path, err)
	}
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
				if now.After(s.ExpiresAt) || (m.idle > 0 && now.Sub(s.LastSeen) > m.idle) {
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

// Stop terminates the reaper goroutine and flushes sessions to disk. It is
// idempotent and safe to call from multiple goroutines.
func (m *SessionManager) Stop() {
	m.stopOnce.Do(func() { close(m.stopCh) })
	m.save()
}
