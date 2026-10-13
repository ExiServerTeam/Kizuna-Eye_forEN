package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Role is the user permission level.
type Role string

const (
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleViewer   Role = "viewer"
)

// rank returns a comparable rank. Larger means more privilege.
func (r Role) rank() int {
	switch r {
	case RoleAdmin:
		return 3
	case RoleOperator:
		return 2
	case RoleViewer:
		return 1
	}
	return 0
}

// AtLeast reports whether r is at least as privileged as min.
func (r Role) AtLeast(min Role) bool {
	return r.rank() >= min.rank()
}

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	return r == RoleAdmin || r == RoleOperator || r == RoleViewer
}

// User is one account. PasswordHash is never serialized to the API.
type User struct {
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	Role         Role      `json:"role"`
	CreatedAt    time.Time `json:"created_at"`
}

// PublicUser is the API-facing view of a user (no hash).
type PublicUser struct {
	Username  string    `json:"username"`
	Role      Role      `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

var (
	// ErrUserExists is returned when adding a duplicate username.
	ErrUserExists = errors.New("username already exists")
	// ErrLastAdmin is returned when deleting/demoting the only admin.
	ErrLastAdmin = errors.New("cannot remove the last admin")
	// ErrUserNotFound is returned when a user does not exist.
	ErrUserNotFound = errors.New("user not found")
	// ErrStoreCorrupt is returned when users.json exists but is unreadable.
	ErrStoreCorrupt = errors.New("users.json が破損しているため操作できません。管理者が手動で修正してください")
)

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,32}$`)

// Store holds users and persists them to a JSON file.
type Store struct {
	mu    sync.RWMutex
	path  string
	users map[string]*User
	// corrupt is set when users.json exists but could not be parsed. In that
	// case NeedsSetup must NOT report true, otherwise anyone could recreate
	// an admin account through /setup after the file is damaged.
	corrupt bool
}

// NewStore creates a Store backed by path.
func NewStore(path string) *Store {
	return &Store{
		path:  path,
		users: make(map[string]*User),
	}
}

// Load reads users.json. A missing file is treated as an empty store.
func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.users = make(map[string]*User)
			return nil
		}
		return err
	}

	// users.json holds bcrypt password hashes; tighten the mode on load so a
	// file created world-readable is not left exposed. Best-effort.
	_ = os.Chmod(s.path, 0600)

	var file struct {
		Users []*User `json:"users"`
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		s.users = make(map[string]*User)
		return nil
	}
	if err := json.Unmarshal(data, &file); err != nil {
		// Mark the store as corrupt so setup is refused. Keep whatever was
		// loaded (nothing) and let the caller log a warning.
		s.corrupt = true
		return fmt.Errorf("users.json のパース失敗: %w", err)
	}

	s.users = make(map[string]*User, len(file.Users))
	for _, u := range file.Users {
		if u == nil || u.Username == "" {
			continue
		}
		s.users[u.Username] = u
	}
	s.corrupt = false
	return nil
}

// IsCorrupt reports whether users.json failed to load.
func (s *Store) IsCorrupt() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.corrupt
}

// saveLocked writes users to disk atomically. Caller must hold s.mu.
func (s *Store) saveLocked() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	list := make([]*User, 0, len(s.users))
	for _, u := range s.users {
		list = append(list, u)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Username < list[j].Username })

	data, err := json.MarshalIndent(map[string]interface{}{"users": list}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	mode := os.FileMode(0600)
	if info, statErr := os.Stat(s.path); statErr == nil {
		mode = info.Mode().Perm()
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

// Count returns the number of users.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.users)
}

// NeedsSetup reports whether the first admin must be created.
// A corrupt store does NOT need setup: recreating an admin over a damaged
// file would be a privilege-escalation path. The operator must fix or
// remove users.json manually instead.
func (s *Store) NeedsSetup() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.corrupt {
		return false
	}
	return len(s.users) == 0
}

// ValidateUsername checks the username format.
func ValidateUsername(name string) error {
	if !usernameRe.MatchString(name) {
		return fmt.Errorf("ユーザー名は英数字・_ . - の3〜32文字で入力してください")
	}
	return nil
}

// Create adds a user with the given plaintext password.
func (s *Store) Create(username, password string, role Role) (*User, error) {
	username = strings.TrimSpace(username)
	if err := ValidateUsername(username); err != nil {
		return nil, err
	}
	if len(password) < 8 {
		return nil, fmt.Errorf("パスワードは8文字以上にしてください")
	}
	if !role.Valid() {
		return nil, fmt.Errorf("ロールが不正です: %s", role)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("パスワードのハッシュ化に失敗: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.corrupt {
		return nil, ErrStoreCorrupt
	}
	if _, exists := s.users[username]; exists {
		return nil, ErrUserExists
	}

	u := &User{
		Username:     username,
		PasswordHash: string(hash),
		Role:         role,
		CreatedAt:    time.Now().UTC(),
	}
	s.users[username] = u
	if err := s.saveLocked(); err != nil {
		delete(s.users, username)
		return nil, err
	}
	return u, nil
}

// Authenticate verifies a username/password pair.
func (s *Store) Authenticate(username, password string) (*User, bool) {
	s.mu.RLock()
	u, ok := s.users[username]
	s.mu.RUnlock()
	if !ok {
		// Compare against a dummy hash to keep timing similar.
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$invalidinvalidinvalidinvalidinvalidinvalidinvalidinvalidinva"), []byte(password))
		return nil, false
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return nil, false
	}
	return u, true
}

// Get returns a user by name.
func (s *Store) Get(username string) (*User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[username]
	return u, ok
}

// List returns all users as public views, sorted by name.
func (s *Store) List() []PublicUser {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PublicUser, 0, len(s.users))
	for _, u := range s.users {
		out = append(out, PublicUser{Username: u.Username, Role: u.Role, CreatedAt: u.CreatedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out
}

// adminCountLocked counts admins. Caller must hold s.mu.
func (s *Store) adminCountLocked() int {
	n := 0
	for _, u := range s.users {
		if u.Role == RoleAdmin {
			n++
		}
	}
	return n
}

// Delete removes a user, refusing to remove the last admin.
func (s *Store) Delete(username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[username]
	if !ok {
		return ErrUserNotFound
	}
	if u.Role == RoleAdmin && s.adminCountLocked() <= 1 {
		return ErrLastAdmin
	}
	delete(s.users, username)
	if err := s.saveLocked(); err != nil {
		s.users[username] = u
		return err
	}
	return nil
}

// ChangePassword updates a user's password.
func (s *Store) ChangePassword(username, password string) error {
	if len(password) < 8 {
		return fmt.Errorf("パスワードは8文字以上にしてください")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("パスワードのハッシュ化に失敗: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[username]
	if !ok {
		return ErrUserNotFound
	}
	prev := u.PasswordHash
	u.PasswordHash = string(hash)
	if err := s.saveLocked(); err != nil {
		u.PasswordHash = prev
		return err
	}
	return nil
}

// ChangeRole updates a user's role, refusing to demote the last admin.
func (s *Store) ChangeRole(username string, role Role) error {
	if !role.Valid() {
		return fmt.Errorf("ロールが不正です: %s", role)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[username]
	if !ok {
		return ErrUserNotFound
	}
	if u.Role == RoleAdmin && role != RoleAdmin && s.adminCountLocked() <= 1 {
		return ErrLastAdmin
	}
	prev := u.Role
	u.Role = role
	if err := s.saveLocked(); err != nil {
		u.Role = prev
		return err
	}
	return nil
}
