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

// maxSessions はインメモリセッションの総数を制限する。上限が無いと、未認証
// クライアントがゲストログインエンドポイントをループで呼び、セッションマップを
// 無制限に成長させ得る（メモリ枯渇 DoS）。
// maxSessions bounds the total number of in-memory sessions. Without a cap, an
// unauthenticated client could call the guest-login endpoint in a loop and grow
// the session map without bound (memory-exhaustion DoS).
const maxSessions = 4096

// ErrTooManySessions は、ストアが満杯でどのセッションも追い出せないため新しい
// セッションを作成できないときに返される。
// ErrTooManySessions is returned when a new session cannot be created because
// the store is full and no session could be evicted.
var ErrTooManySessions = errors.New("セッション数が上限に達しました")

// Session は 1 つの認証済みブラウザセッションだ。
// Session is one authenticated browser session.
type Session struct {
	// ID は生のベアラトークンだ。cookie でのみ配信され、決してディスクへ
	// 書かれない（json:"-"）。インメモリマップと永続化ファイルは代わりに
	// sha256(ID) でキー付けされるため、漏洩したセッションファイルで
	// セッションを乗っ取ることができない。
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

// SessionManager はセッションを保存する。セッションはメモリ内とディスク上で
// sha256(token) でキー付けされ、生のトークンはブラウザ cookie にのみ存在する。
// SessionManager stores sessions. Sessions are keyed in memory and on disk by
// sha256(token); the raw token lives only in the browser cookie.
type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*Session // key = sha256 hex of the raw token
	ttl      time.Duration
	idle     time.Duration
	max      int

	// path は永続化ファイルだ。空は永続化を無効化する（メモリのみ、再起動で
	// セッションは失われる）。
	// path is the persistence file. Empty disables persistence (in-memory
	// only, sessions lost on restart).
	path string
	// ipBind はセッションを生成元 IP に束縛する（任意）。
	// ipBind binds a session to the IP that created it (optional).
	ipBind bool
	// stopOnce は Stop を冪等かつ並行呼び出しで安全にする。置き換えた
	// select/close パターンは stopCh を 2 回 close して panic し得た。
	// stopOnce makes Stop idempotent and safe under concurrent calls: the
	// select/close pattern it replaced could close stopCh twice and panic.
	stopOnce sync.Once
	// logf を設定すると永続化失敗メッセージを受け取る。これが無いと、ディスク
	// 満杯や権限エラーが黙ってセッション永続化を止めてしまう。
	// logf, when set, receives persistence-failure messages. Without it a
	// full disk or permission error would silently stop persisting sessions.
	logf func(format string, args ...interface{})

	// saveMu はディスク書き込みを直列化する。save() は m.mu 下でセッションマップを
	// スナップショットし、それを解放してからファイルを書く。saveMu が無いと
	// 2 つの並行 save が割り込み、後から始まった方が先に終わって古いスナップショットの
	// rename が勝ち、新しいセッションがディスクから落ち得る（更新の消失）。
	// saveMu をスナップショット+書き込みにわたって保持することで、各 save が最新
	// 状態を書き、書き込みが順序付けられる。
	// saveMu serializes disk writes. save() snapshots the session map under
	// m.mu, releases it, then writes the file; without saveMu two concurrent
	// saves could interleave so that the later-started one finishes first and
	// the older snapshot's rename wins, dropping the newer sessions from disk
	// (lost update). Holding saveMu across snapshot+write makes each save
	// write the newest state and keeps writes ordered.
	saveMu sync.Mutex

	stopCh chan struct{}
}

// SetLogger は永続化エラーを報告するのに使うログ関数を設定する。
// SetLogger installs a logging function used to report persistence errors.
func (m *SessionManager) SetLogger(logf func(format string, args ...interface{})) {
	m.mu.Lock()
	m.logf = logf
	m.mu.Unlock()
}

// log は任意のロガーを通じてメッセージを永続化する。
// log persists a message through the optional logger.
func (m *SessionManager) log(format string, args ...interface{}) {
	m.mu.RLock()
	fn := m.logf
	m.mu.RUnlock()
	if fn != nil {
		fn(format, args...)
	}
}

// NewSessionManager は SessionManager を作る。
// ttl は絶対寿命、idle は無操作タイムアウトだ。
// NewSessionManager creates a SessionManager.
// ttl is the absolute lifetime; idle is the inactivity timeout.
func NewSessionManager(ttl, idle time.Duration) *SessionManager {
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	// idle == 0 は既定（2h）を選ぶ。負値は無操作タイムアウトを完全に無効化し、
	// セッションは絶対 TTL まで有効のままになる。これにより運用者は、絶対期限を
	// 安全網として残しつつ「戻ってきたらログアウトされていた」挙動を止められる。
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

// EnablePersistence は path でセッション永続化を（任意で IP 束縛も）有効化する。
// まず既存セッションをロードする。空の path はマネージャをメモリのみに保つ。
// リクエストを処理し始める前の起動中に 1 回呼ぶ。
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

// IPBindEnabled はセッションがクライアント IP に束縛されているかを返す。
// IPBindEnabled reports whether sessions are bound to their client IP.
func (m *SessionManager) IPBindEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.ipBind
}

// hashToken は生のセッショントークンの 16 進 sha256 を返す。
// hashToken returns the hex sha256 of a raw session token.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// newID はランダムな 32 バイト 16 進セッショントークンを返す。
// newID returns a random 32-byte hex session token.
func newID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Create はユーザーの新しいセッションを発行する。IP 束縛を使わない場合、ip は
// 空でよい。
// Create issues a new session for the user. ip may be empty when IP binding is
// not used.
func (m *SessionManager) Create(username string, role Role, ip string) (*Session, error) {
	return m.CreateWithTTL(username, role, ip, m.ttl)
}

// CreateWithTTL は明示的な絶対寿命を持つ Create だ。ゲストセッションに使う。
// ゲストは匿名の使い捨て viewer なので、実ユーザーの TTL（例: 30 日）いっぱい
// 有効にすると無用なセッションがストアとディスクに残る。ttl <= 0 はマネージャの
// 設定 TTL へフォールバックする。
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
	// 未認証のゲストログイン洪水がメモリを枯渇させないようストアを制限する。
	// 最も最近使われていないセッション（ゲストセッションを優先）を追い出す。
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

// evictLocked は場所を作るために 1 つのセッションを除去する。アイドルのゲスト
// セッションを優先し、次に最も最近見られていないセッションへフォールバックする。
// 呼び出し側は m.mu を保持すること。除去するものが無い場合のみ false を返す。
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

// Get はセッションが有効ならそれを返す。無操作タイマーを更新する。
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
	// 返すコピーに生のトークンを戻す。ディスクからロードしたセッションは
	// sha256(token) でキー付けされ、その ID フィールドは空だ。これが無いと、
	// 再起動後に Delete(session.ID) を行う呼び出し側が Delete("") を呼び、
	// サーバー側セッションの除去に失敗する（ログアウトと、ログイン時の
	// セッション固定化防御が黙って何もしなくなる）。
	// Restore the raw token on the returned copy. Sessions loaded from disk
	// are keyed by sha256(token) and their ID field is empty, so without this
	// a caller doing Delete(session.ID) after a restart would call
	// Delete("") and fail to remove the server-side session (logout and the
	// login-time session-fixation defense would silently do nothing).
	copy.ID = token
	m.mu.Unlock()
	return &copy, true
}

// Delete はセッションを除去する（ログアウト）。
// Delete removes a session (logout).
func (m *SessionManager) Delete(token string) {
	key := hashToken(token)
	m.mu.Lock()
	delete(m.sessions, key)
	m.mu.Unlock()
	m.save()
}

// DeleteUserSessions はユーザー名の全セッションを除去する。
// ユーザーが削除された、またはパスワード/ロールが変更されたときに使う。
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

// DeleteAll は全セッションを除去する（例: 自身のロール/パスワード変更後）。
// DeleteAll removes every session (e.g. after a role/password change on self).
func (m *SessionManager) DeleteAll() {
	m.mu.Lock()
	m.sessions = make(map[string]*Session)
	m.mu.Unlock()
	m.save()
}

// persistedSession はディスク上の 1 レコードだ。生のトークンは存在せず、Hash は
// トークンの sha256 で、再起動後の参照が依然機能するようにする。
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

// load は永続化ファイル（あれば）を読む。ファイル欠落はエラーではない。
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
	// ファイルはセッションハッシュを保持する。所有者のみに保つ。
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
		// 期限切れレコードを落とし、古いファイルがそれらを復活させないようにする。
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

// save はセッションテーブルをアトミックに書く。永続化が無効なときは no-op だ。
// 呼び出し側は m.mu を保持してはならない。
// save writes the session table atomically. It is a no-op when persistence is
// disabled. Callers must NOT hold m.mu.
func (m *SessionManager) save() {
	// 遅い save が新しいものを上書きしないよう（更新の消失）書き手を直列化する。
	// saveMu はロガー呼び出しの前に解放する。ロガーは m.mu のみを取るため、
	// ロック順序の逆転は無い。
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
	// fsutil.WriteFileAtomic: prepare the parent directory as 0700, fsync a
	// 0600 temp file, then rename (L-12: the same logic was duplicated here).
	if err := fsutil.WriteFileAtomic(path, data, 0600); err != nil {
		m.log("セッション永続化: 書き込みに失敗 (%s): %v", path, err)
	}
}

// reaper は定期的に期限切れセッションを除去し、LastSeen の更新をディスクへ
// フラッシュして、無操作タイムアウトが再起動をまたいで維持されるようにする。
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
			// 少なくとも定期的に永続化し、ディスク上の LastSeen を進める。
			// Persist at least periodically so LastSeen advances on disk.
			_ = changed
			m.save()
		}
	}
}

// Stop は reaper ゴルーチンを終了し、セッションをディスクへフラッシュする。
// 冪等で、複数ゴルーチンから呼んでも安全だ。
// Stop terminates the reaper goroutine and flushes sessions to disk. It is
// idempotent and safe to call from multiple goroutines.
func (m *SessionManager) Stop() {
	m.stopOnce.Do(func() { close(m.stopCh) })
	m.save()
}
