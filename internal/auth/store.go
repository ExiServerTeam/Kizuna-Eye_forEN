package auth

import (
	"crypto/hmac"
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

	"Kizuna-Eye/pkg/fsutil"
)

// Role はユーザーの権限レベルだ。
// Role is the user permission level.
type Role string

const (
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleViewer   Role = "viewer"
)

// rank は比較可能なランクを返す。大きいほど権限が高い。
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

// AtLeast は r が min 以上の権限かを返す。
// AtLeast reports whether r is at least as privileged as min.
func (r Role) AtLeast(min Role) bool {
	return r.rank() >= min.rank()
}

// Valid は r が既知のロールかを返す。
// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	return r == RoleAdmin || r == RoleOperator || r == RoleViewer
}

// User は 1 アカウントだ。PasswordHash は API へ決してシリアライズされない。
// User is one account. PasswordHash is never serialized to the API.
type User struct {
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	Role         Role      `json:"role"`
	CreatedAt    time.Time `json:"created_at"`
	// Avatar はアバターディレクトリ内のユーザーのアバターのファイル名だ
	// （アバターが無い場合は空）。
	// Avatar is the file name of the user's avatar inside the avatar
	// directory (empty when the user has no avatar).
	Avatar string `json:"avatar,omitempty"`
}

// PublicUser はユーザーの API 向けビューだ（ハッシュ無し）。
// PublicUser is the API-facing view of a user (no hash).
type PublicUser struct {
	Username  string    `json:"username"`
	Role      Role      `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	Avatar    string    `json:"avatar,omitempty"`
}

var (
	// ErrUserExists は重複ユーザー名の追加時に返される。
	// ErrUserExists is returned when adding a duplicate username.
	ErrUserExists = errors.New("username already exists")
	// ErrLastAdmin は唯一の admin の削除/降格時に返される。
	// ErrLastAdmin is returned when deleting/demoting the only admin.
	ErrLastAdmin = errors.New("cannot remove the last admin")
	// ErrUserNotFound はユーザーが存在しないときに返される。
	// ErrUserNotFound is returned when a user does not exist.
	ErrUserNotFound = errors.New("user not found")
	// ErrStoreCorrupt は users.json が存在するが読めないときに返される。
	// ErrStoreCorrupt is returned when users.json exists but is unreadable.
	ErrStoreCorrupt = errors.New("users.json が破損しているため操作できません。管理者が手動で修正してください")
)

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,32}$`)

// dummyPasswordHash は、未知ユーザーのログイン試行のタイミングを等しくするために
// 使う、構文的に「有効な」bcrypt ハッシュだ。きれいにパースされなければならない。
// bcrypt.CompareHashAndPassword は（高コストな）鍵導出を行う前に不正なハッシュを
// 拒否するため、"$2a$10$invalid..." のようなプレースホルダはほぼ即座に返り、
// 攻撃者が応答時間で未知のユーザー名と既知のものを区別できてしまう
// （ユーザー名列挙）。このハッシュは cost 10 で、ユーザー作成時に使う
// bcrypt.DefaultCost と同じなので、両経路が同等の仕事をする。それが符号化する値は
// 無関係だ（結果は破棄される）。
// dummyPasswordHash is a syntactically VALID bcrypt hash used to equalise the
// timing of a login attempt for an unknown user. It must parse cleanly:
// bcrypt.CompareHashAndPassword rejects a malformed hash before doing the
// (expensive) key derivation, so a placeholder like "$2a$10$invalid..." would
// return almost instantly and let an attacker tell an unknown username from a
// known one by response time (username enumeration). This hash has cost 10,
// the same as bcrypt.DefaultCost used when creating users, so both paths do
// equivalent work. The value it encodes is irrelevant: the result is discarded.
const dummyPasswordHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

// Store はユーザーを保持し、JSON ファイルへ永続化する。
// Store holds users and persists them to a JSON file.
type Store struct {
	mu    sync.RWMutex
	path  string
	users map[string]*User
	// corrupt は users.json が存在するがパースできなかったときに設定される。
	// その場合 NeedsSetup は true を報告してはならない。そうしないと、
	// ファイル破損後に誰でも /setup から admin アカウントを再作成できてしまう。
	// corrupt is set when users.json exists but could not be parsed. In that
	// case NeedsSetup must NOT report true, otherwise anyone could recreate
	// an admin account through /setup after the file is damaged.
	corrupt bool
	// macKey は users.json の MAC を計算・検証する HMAC 鍵だ。nil なら完全性検査は
	// 無効（EnableIntegrity 未呼び出し）。
	// macKey is the HMAC key for the users.json MAC. nil disables integrity
	// checking (EnableIntegrity was not called).
	macKey []byte
	// unsigned は読み込んだ users.json に MAC が無かった（旧形式）ことを示す。
	// 次の保存で MAC が付く。
	// unsigned marks that the loaded users.json had no MAC (legacy). The next
	// save adds one.
	unsigned bool
}

// NewStore は path に裏打ちされた Store を作る。
// NewStore creates a Store backed by path.
func NewStore(path string) *Store {
	return &Store{
		path:  path,
		users: make(map[string]*User),
	}
}

// Load は users.json を読む。ファイル欠落は空のストアとして扱う。
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

	// users.json は bcrypt パスワードハッシュを保持する。読み込み時にモードを
	// 締め、world-readable で作られたファイルを露出したままにしない。ベストエフォート。
	// users.json holds bcrypt password hashes; tighten the mode on load so a
	// file created world-readable is not left exposed. Best-effort.
	_ = os.Chmod(s.path, 0600)

	var file struct {
		Users []*User `json:"users"`
		Mac   string  `json:"mac"`
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		s.users = make(map[string]*User)
		return nil
	}
	if err := json.Unmarshal(data, &file); err != nil {
		// ストアを破損としてマークし、セットアップを拒否する。ロードできたもの
		// （何も無い）は保ち、呼び出し側に警告をログさせる。
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

	// MAC があれば検証する。不一致は改ざんとして扱い、セットアップも変更も
	// 拒否する（fail-closed）。MAC が無い旧形式は警告扱い（unsigned）とし、
	// 次の保存で署名する。MAC があるのに鍵が無い場合は検証できないため
	// 同じく破損として拒否する。
	// Verify the MAC when present. A mismatch is tampering: refuse setup and
	// mutations (fail-closed). A missing MAC is a legacy file (unsigned); the
	// next save signs it. A MAC with no key cannot be verified, so it is also
	// refused as corrupt.
	s.unsigned = file.Mac == ""
	if file.Mac != "" {
		if s.macKey == nil {
			s.corrupt = true
			return fmt.Errorf("users.json に MAC があるが鍵が未設定です")
		}
		canonical, cerr := canonicalUsers(s.users)
		if cerr != nil {
			s.corrupt = true
			return fmt.Errorf("users.json の正規化に失敗: %w", cerr)
		}
		want := computeUsersMAC(s.macKey, canonical)
		if !hmac.Equal([]byte(want), []byte(file.Mac)) {
			s.corrupt = true
			return ErrUsersMACMismatch
		}
	}

	s.corrupt = false
	return nil
}

// EnableIntegrity は users.json の MAC 検証を有効化する。keyPath の鍵を読み込み
// （無ければ生成し）、以降の保存で MAC を付与する。Load より前に呼ぶこと。
// EnableIntegrity turns on MAC verification for users.json. It loads (or
// creates) the key at keyPath; subsequent saves attach a MAC. Call before Load.
func (s *Store) EnableIntegrity(keyPath string) error {
	key, err := LoadOrCreateUsersKey(keyPath)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.macKey = key
	s.mu.Unlock()
	return nil
}

// IsUnsigned は読み込んだ users.json に MAC が無かった（旧形式）かを返す。
// IsUnsigned reports whether the loaded users.json had no MAC (legacy form).
func (s *Store) IsUnsigned() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.unsigned
}

// IsCorrupt は users.json のロードが失敗したかを返す。
// IsCorrupt reports whether users.json failed to load.
func (s *Store) IsCorrupt() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.corrupt
}

// saveLocked はユーザーをディスクへアトミックに書く。呼び出し側は s.mu を保持すること。
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

	payload := map[string]interface{}{"users": list}
	// 完全性検査が有効なら、正規化したユーザー一覧に対する MAC を埋め込む。
	// If integrity is enabled, embed a MAC over the canonical users list.
	if s.macKey != nil {
		canonical, cerr := canonicalUsers(s.users)
		if cerr != nil {
			return cerr
		}
		payload[usersMACField] = computeUsersMAC(s.macKey, canonical)
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	mode := os.FileMode(0600)
	if info, statErr := os.Stat(s.path); statErr == nil {
		mode = info.Mode().Perm()
	}

	// fsutil.WriteFileAtomic: mode を一時ファイルへ適用し、fsync してから
	// rename する（L-12: 同じ処理が12箇所に重複していた）。
	// fsutil.WriteFileAtomic: apply the mode to a temp file, fsync, then
	// rename (L-12: the same logic was duplicated in twelve places).
	return fsutil.WriteFileAtomic(s.path, data, mode)
}

// Count はユーザー数を返す。
// Count returns the number of users.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.users)
}

// NeedsSetup は最初の admin を作成すべきかを返す。
// 破損したストアはセットアップを要さない。破損ファイルの上に admin を再作成すると
// 権限昇格の経路になる。代わりに運用者が users.json を手で修正または除去する
// 必要がある。
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

// ValidateUsername はユーザー名の形式を検証する。
// ValidateUsername checks the username format.
func ValidateUsername(name string) error {
	if !usernameRe.MatchString(name) {
		return fmt.Errorf("ユーザー名は英数字・_ . - の3〜32文字で入力してください")
	}
	return nil
}

// Create は与えられた平文パスワードでユーザーを追加する。
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
	// 生きた *User ではなくコピーを返す。ポインタを保つ呼び出し側は、そうしないと
	// 書き込みロック下でそれを書く ChangePassword/ChangeRole と競合し得る。
	// Return a copy, not the live *User: a caller that keeps the pointer
	// could otherwise race with ChangePassword/ChangeRole writing it under
	// the write lock.
	cp := *u
	return &cp, nil
}

// Authenticate はユーザー名/パスワードの組を検証する。
//
// ユーザーの「コピー」を返し、ストアの生きた *User は決して返さない。ハッシュも
// 読み取りロック保持中に捕捉する。RUnlock 後に u.PasswordHash を読むと、書き込み
// ロック下で同じフィールドを書く並行の ChangePassword/ChangeRole と競合する
// （レース検出器が指摘するデータ競合）。
// Authenticate verifies a username/password pair.
//
// It returns a COPY of the user, never the store's live *User. The hash is
// also captured while the read lock is held: reading u.PasswordHash after
// RUnlock would race with a concurrent ChangePassword/ChangeRole that writes
// the same fields under the write lock (a data race the race detector flags).
func (s *Store) Authenticate(username, password string) (*User, bool) {
	s.mu.RLock()
	u, ok := s.users[username]
	if !ok {
		s.mu.RUnlock()
		// 有効なダミーハッシュと比較し、未知のユーザー名が既知ユーザーの誤った
		// パスワードと同じコストになるようにする（タイミングベースのユーザー名
		// 列挙を防ぐ）。
		// Compare against a valid dummy hash so an unknown username costs the
		// same as a wrong password for a known user (prevents timing-based
		// username enumeration).
		_ = bcrypt.CompareHashAndPassword([]byte(dummyPasswordHash), []byte(password))
		return nil, false
	}
	hash := u.PasswordHash
	cp := *u
	s.mu.RUnlock()

	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return nil, false
	}
	return &cp, true
}

// Get は名前でユーザーのコピーを返す。
// Get returns a copy of a user by name.
func (s *Store) Get(username string) (*User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[username]
	if !ok {
		return nil, false
	}
	cp := *u
	return &cp, true
}

// SetAvatar はユーザーのアバターファイル名を記録する（クリアは空）。
// SetAvatar records the user's avatar file name (empty to clear).
func (s *Store) SetAvatar(username, filename string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[username]
	if !ok {
		return ErrUserNotFound
	}
	prev := u.Avatar
	u.Avatar = filename
	if err := s.saveLocked(); err != nil {
		u.Avatar = prev
		return err
	}
	return nil
}

// AvatarOf はユーザーのアバターファイル名、または "" を返す。
// AvatarOf returns the avatar file name for a user, or "".
func (s *Store) AvatarOf(username string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if u, ok := s.users[username]; ok {
		return u.Avatar
	}
	return ""
}

// List は全ユーザーを公開ビューとして返し、名前順にソートする。
// List returns all users as public views, sorted by name.
func (s *Store) List() []PublicUser {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PublicUser, 0, len(s.users))
	for _, u := range s.users {
		out = append(out, PublicUser{Username: u.Username, Role: u.Role, CreatedAt: u.CreatedAt, Avatar: u.Avatar})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out
}

// adminCountLocked は admin を数える。呼び出し側は s.mu を保持すること。
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

// Delete はユーザーを除去し、唯一の admin の除去を拒否する。
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

// ChangePassword はユーザーのパスワードを更新する。
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

// ChangeRole はユーザーのロールを更新し、唯一の admin の降格を拒否する。
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
