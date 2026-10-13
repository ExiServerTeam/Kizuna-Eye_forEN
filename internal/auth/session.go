package auth

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Session is one authenticated browser session.
type Session struct {
	ID        string
	Username  string
	Role      Role
	CreatedAt time.Time
	LastSeen  time.Time
	ExpiresAt time.Time
}

// SessionManager stores sessions in memory.
// Sessions are lost on restart by design (see the design memo).
type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	ttl      time.Duration
	idle     time.Duration

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
		stopCh:   make(chan struct{}),
	}
	go m.reaper()
	return m
}

// newID returns a random 32-byte hex session ID.
func newID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Create issues a new session for the user.
func (m *SessionManager) Create(username string, role Role) (*Session, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	s := &Session{
		ID:        id,
		Username:  username,
		Role:      role,
		CreatedAt: now,
		LastSeen:  now,
		ExpiresAt: now.Add(m.ttl),
	}
	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()
	return s, nil
}

// Get returns a session if it is valid. It refreshes the idle timer.
func (m *SessionManager) Get(id string) (*Session, bool) {
	if id == "" {
		return nil, false
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, false
	}
	if now.After(s.ExpiresAt) || now.Sub(s.LastSeen) > m.idle {
		delete(m.sessions, id)
		return nil, false
	}
	s.LastSeen = now
	copy := *s
	return &copy, true
}

// Delete removes a session (logout).
func (m *SessionManager) Delete(id string) {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
}

// DeleteUserSessions removes every session for a username.
// Used when a user is deleted or has their password/role changed.
func (m *SessionManager) DeleteUserSessions(username string) {
	m.mu.Lock()
	for id, s := range m.sessions {
		if s.Username == username {
			delete(m.sessions, id)
		}
	}
	m.mu.Unlock()
}

// DeleteAll removes every session (e.g. after a role/password change on self).
func (m *SessionManager) DeleteAll() {
	m.mu.Lock()
	m.sessions = make(map[string]*Session)
	m.mu.Unlock()
}

// reaper periodically removes expired sessions.
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
			for id, s := range m.sessions {
				if now.After(s.ExpiresAt) || now.Sub(s.LastSeen) > m.idle {
					delete(m.sessions, id)
				}
			}
			m.mu.Unlock()
		}
	}
}

// Stop terminates the reaper goroutine.
func (m *SessionManager) Stop() {
	select {
	case <-m.stopCh:
	default:
		close(m.stopCh)
	}
}
