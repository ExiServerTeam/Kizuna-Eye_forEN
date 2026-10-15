package auth

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// maxLimiterEntries は IP 単位の試行マップを制限する。ログイン試行は多数の
// （偽装され得る）送信元アドレスから到着し得るため、無制限のマップは緩やかな
// メモリ枯渇の経路になる（G-5）。
// maxLimiterEntries bounds the per-IP attempt map. Login attempts can arrive
// from many (possibly spoofed) source addresses, so an unbounded map is a slow
// memory-exhaustion vector (G-5).
const maxLimiterEntries = 10000

// loginLimiter はクライアント IP ごとに失敗したログイン試行を抑制する。
// 単純なインメモリのスライディングウィンドウだ。window 内に maxFailures 回の
// 失敗があると、その IP は lockDuration の間ロックアウトされる。
// loginLimiter throttles failed login attempts per client IP.
// It is a simple in-memory sliding window: after maxFailures failures
// within window, the IP is locked out for lockDuration.
type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string]*attemptInfo
	max      int
	window   time.Duration
	lockout  time.Duration

	stopCh chan struct{}
	stopOn sync.Once
}

type attemptInfo struct {
	count      int
	firstAt    time.Time
	lockedTill time.Time
}

func newLoginLimiter(max int, window, lockout time.Duration) *loginLimiter {
	if max <= 0 {
		max = 10
	}
	if window <= 0 {
		window = 5 * time.Minute
	}
	if lockout <= 0 {
		lockout = 5 * time.Minute
	}
	l := &loginLimiter{
		attempts: make(map[string]*attemptInfo),
		max:      max,
		window:   window,
		lockout:  lockout,
		stopCh:   make(chan struct{}),
	}
	go l.reaper()
	return l
}

// Stop は reaper ゴルーチンを終了する。冪等だ。
// Stop terminates the reaper goroutine. It is idempotent.
func (l *loginLimiter) Stop() {
	l.stopOn.Do(func() { close(l.stopCh) })
}

// clientIP はレート制限用のクライアント IP を抽出する。
//
// X-Forwarded-For は、リクエストがループバックアドレス（同一ホスト上の
// リバースプロキシ）から到着した場合にのみ尊重する。任意のピアからの XFF を
// 信頼すると、攻撃者がリクエストごとに異なる X-Forwarded-For 値を送って
// ログインレート制限を回避できてしまう。
//
// さらに、リストの「右端（最後）」のエントリだけを採用する。信頼できる
// 同一ホストのリバースプロキシは、クライアントが送った値を先頭に残し、実
// クライアントアドレスを末尾に追記する（nginx の既定
// $proxy_add_x_forwarded_for）。左端は攻撃者が自由に偽装できるため、それを
// キーにすると「リクエストごとに XFF を変える」だけでレート制限を無効化できる
// （XFF バイパス）。右端はプロキシが付けた値なので、その偽装が効かない。
// 単一の同一ホストプロキシという推奨構成を前提とする。プロキシが上書き
// （proxy_set_header X-Forwarded-For $remote_addr）する場合も、要素は 1 つ
// なので右端＝実クライアントで正しい。
// clientIP extracts the client IP for rate limiting.
//
// X-Forwarded-For is only honored when the request arrives from a loopback
// address, i.e. a reverse proxy on the same host. Trusting XFF from an
// arbitrary peer would let an attacker bypass the login rate limit by
// sending a different X-Forwarded-For value on every request.
//
// Only the RIGHTMOST entry is used. A trusted same-host reverse proxy keeps
// the client-supplied values at the front and appends the real client
// address at the end (nginx's default $proxy_add_x_forwarded_for). The
// leftmost entry is attacker-controlled, so keying on it lets a client
// disable the limit just by varying XFF (an XFF bypass). The rightmost entry
// is the proxy's own, so that spoof does not work. This assumes the
// recommended single same-host proxy; when the proxy overwrites
// (proxy_set_header X-Forwarded-For $remote_addr) there is one element, so
// the rightmost is still the real client.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}

	// ループバックのピア（ローカルリバースプロキシ）だけが X-Forwarded-For を
	// 設定できる。
	// Only a loopback peer (local reverse proxy) may set X-Forwarded-For.
	if isLoopback(host) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// 右端（最後）の有効な IP を採用する。左端は偽装可能なため使わない。
			// Use the rightmost valid IP; the leftmost is spoofable.
			parts := strings.Split(xff, ",")
			for i := len(parts) - 1; i >= 0; i-- {
				if ip := net.ParseIP(strings.TrimSpace(parts[i])); ip != nil {
					return ip.String()
				}
			}
		}
	}
	return host
}

// isLoopback は host がループバックアドレス（127.0.0.0/8 または ::1）かを返す。
// isLoopback reports whether host is a loopback address (127.0.0.0/8 or ::1).
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Allow は ip からのログイン試行を続行してよいかを返す。
// Allow reports whether a login attempt from ip may proceed.
func (l *loginLimiter) Allow(ip string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	info, ok := l.attempts[ip]
	if !ok {
		return true
	}
	if now.Before(info.lockedTill) {
		return false
	}
	// ロックアウトが切れた。失敗カウンタをクリアし、後日の 1 回の失敗が古い
	// ウィンドウがまだ開いている間にすぐ閾値へ達しないようにする。
	// Lockout expired: clear the failure counter so one later failure does
	// not immediately hit the threshold again while the old window is still
	// open.
	if !info.lockedTill.IsZero() {
		info.count = 0
		info.firstAt = now
		info.lockedTill = time.Time{}
		return true
	}
	// ウィンドウが切れた。カウンタをリセットする。
	// Window expired: reset the counter.
	if now.Sub(info.firstAt) > l.window {
		delete(l.attempts, ip)
		return true
	}
	return true
}

// RecordFailure は失敗カウンタを増やし、閾値に達したらロックアウトする。
// RecordFailure increments the failure count and locks out when the
// threshold is reached.
func (l *loginLimiter) RecordFailure(ip string) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	info, ok := l.attempts[ip]
	if !ok || now.Sub(info.firstAt) > l.window {
		info = &attemptInfo{count: 0, firstAt: now}
		l.attempts[ip] = info
	}
	info.count++
	if info.count >= l.max {
		info.lockedTill = now.Add(l.lockout)
	}
	if len(l.attempts) > maxLimiterEntries {
		l.evictLocked(now)
	}
}

// evictLocked は期限切れエントリを落とし、それでもマップが大きすぎれば最も古い
// ものを落とす。これにより、異なる送信元アドレスの洪水下でもサイズが有界に保たれる（G-5）。
// evictLocked drops expired entries and, if the map is still too large, the
// oldest ones, so its size stays bounded even under a flood of distinct source
// addresses (G-5).
func (l *loginLimiter) evictLocked(now time.Time) {
	for ip, info := range l.attempts {
		if now.After(info.lockedTill) && now.Sub(info.firstAt) > l.window {
			delete(l.attempts, ip)
		}
	}
	for len(l.attempts) > maxLimiterEntries {
		var oldestIP string
		var oldest time.Time
		first := true
		for ip, info := range l.attempts {
			if first || info.firstAt.Before(oldest) {
				oldest = info.firstAt
				oldestIP = ip
				first = false
			}
		}
		if oldestIP == "" {
			return
		}
		delete(l.attempts, oldestIP)
	}
}

// Hit は ip への試行を 1 回記録し、上限に達したらロックアウトする。
// これは RecordFailure だが、成功したが濫用的な呼び出し（例: ゲストログインの
// スパム）にも合う名前で、依然レート制限すべきものだ。
// Hit records one attempt against ip and locks out when the limit is reached.
// It is RecordFailure under a name that also fits successful-but-abusive
// calls (e.g. guest-login spam) that should still be rate limited.
func (l *loginLimiter) Hit(ip string) {
	l.RecordFailure(ip)
}

// Reset はログイン成功後に ip の失敗記録をクリアする。
// Reset clears the failure record for ip after a successful login.
func (l *loginLimiter) Reset(ip string) {
	l.mu.Lock()
	delete(l.attempts, ip)
	l.mu.Unlock()
}

// reaper は Stop が呼ばれるまで定期的に古いエントリを除去する。
// reaper periodically removes stale entries until Stop is called.
func (l *loginLimiter) reaper() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-l.stopCh:
			return
		case <-ticker.C:
			now := time.Now()
			l.mu.Lock()
			for ip, info := range l.attempts {
				if now.After(info.lockedTill) && now.Sub(info.firstAt) > l.window {
					delete(l.attempts, ip)
				}
			}
			l.mu.Unlock()
		}
	}
}
