package logger

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// forwardQueueSize はログ転送のメモリ内バックログ上限だ。コレクタが遅い・
// 到達不能でもメモリが無制限に増えることはなく、アプリのログ経路がネットワークで
// ブロックされることもない。
// forwardQueueSize bounds the in-memory backlog of a log forwarder. A slow or
// unreachable collector can therefore never grow memory without bound, and the
// application's logging path is never blocked by the network.
const forwardQueueSize = 256

// forwardCloseGrace は Close が送信中の 1 行を待つ上限だ。死んだコレクタで
// シャットダウンが遅延しないようにする。
// forwardCloseGrace bounds how long Close waits for the in-flight line to be
// sent, so shutdown is not delayed by a dead collector.
const forwardCloseGrace = 2 * time.Second

// forwardWriter はログ行をリモート HTTP(S) エンドポイントへ転送する。opt-in であり、
// 呼び出し側を決してブロックしない。有界キューが満杯になると、ログを止めるのではなく
// 行を破棄する（そして数える）。リダイレクトは追わない。侵害されたコレクタが
// （機微な可能性のある）ログ行のストリームを別の宛先へ転送できてはならない。
//
// pkg/logger は host 専用（どのプラグインもインポートしない）なので、このファイルの
// 追加はプラグインのビルドハッシュを変えず、再ビルドを強制しない。
// forwardWriter mirrors log lines to a remote HTTP(S) endpoint. It is opt-in
// and never blocks the caller: when the bounded queue is full the line is
// dropped (and counted) rather than stalling logging. Redirects are NOT
// followed: a compromised collector must not be able to redirect the stream of
// (potentially sensitive) log lines to another destination.
//
// pkg/logger is host-only (no plugin imports it), so adding this file does not
// change any plugin's build hash and does not force a plugin rebuild.
type forwardWriter struct {
	url    string
	client *http.Client
	ch     chan []byte
	done   chan struct{}

	closeOnce sync.Once
	closed    atomic.Bool
	dropped   atomic.Uint64

	// sendMu は Write と Close の競合を閉じる。closed フラグ単独では TOCTOU 検査に
	// なる。Write が closed.Load()==false を通過し、Close が走り（closed 設定 + ch を close）、
	// その後 Write が closed チャネルへ送って panic し得る。Write は検査と送信の間ずっと
	// 読み取りロックを保持し、Close は close の間ずっと書き込みロックを保持するため、
	// 送信が close と割り込むことはない。Write はロック下で決してブロックしない
	// （default 付き select）ので、Close が枯渇することもない。
	// sendMu closes the race between Write and Close. The closed flag alone
	// is a TOCTOU check: a Write could pass closed.Load()==false, then Close
	// could run (set closed + close(ch)), then the Write would send on a
	// closed channel and panic. Write holds the read lock for the whole
	// check-and-send, and Close holds the write lock while closing, so no
	// send can interleave with the close. Write never blocks under the lock
	// (select with default), so Close cannot be starved.
	sendMu sync.RWMutex
}

// newForwardWriter は rawURL を検証し、バックグラウンド送信側を起動する。
// 不正な URL（誤ったスキーム、ホスト無し）はエラーにし、呼び出し側が
// 黙ってどこにも転送せずに済むよう明確な警告をログできるようにする。
// newForwardWriter validates rawURL and starts the background sender. An
// invalid URL (wrong scheme, no host) is an error so the caller can log a
// clear warning instead of silently forwarding nowhere.
func newForwardWriter(rawURL string, timeout time.Duration) (*forwardWriter, error) {
	s := strings.TrimSpace(rawURL)
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("log_forward.url を解析できません: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("log_forward.url は http/https のみ: %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("log_forward.url にホストがありません")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	fw := &forwardWriter{
		url:  s,
		ch:   make(chan []byte, forwardQueueSize),
		done: make(chan struct{}),
		client: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	go fw.run()
	return fw, nil
}

func (fw *forwardWriter) run() {
	for line := range fw.ch {
		fw.post(line)
	}
	close(fw.done)
}

func (fw *forwardWriter) post(line []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), fw.client.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fw.url, bytes.NewReader(line))
	if err != nil {
		fw.dropped.Add(1)
		return
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	resp, err := fw.client.Do(req)
	if err != nil {
		fw.dropped.Add(1)
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		fw.dropped.Add(1)
	}
}

// Write は p のコピーをキューに積み（呼び出し側のバッファは再利用される）、
// 常に成功を返す。これにより転送失敗がファイル/stdout ログを壊すことがない。
// Write enqueues a copy of p (the caller's buffer is reused) and always
// returns success so a forward failure can never break file/stdout logging.
func (fw *forwardWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	// Close 後の書き込みを拒否する。そのとき送信側チャネルは閉じており、
	// 送ると panic する。closed 検査と送信の両方にわたり読み取りロックを保持し、
	// Close（close の前に書き込みロックを取る）がその間に割り込めないようにする
	// （closed チャネルへの送信）。
	// Refuse writes after Close: the sender channel is closed then, and a send
	// would panic. Hold the read lock across both the closed check and the
	// send so Close (which takes the write lock before closing) cannot
	// interleave between them (send-on-closed-channel).
	fw.sendMu.RLock()
	defer fw.sendMu.RUnlock()
	if fw.closed.Load() {
		return len(p), nil
	}
	buf := make([]byte, len(p))
	copy(buf, p)
	select {
	case fw.ch <- buf:
	default:
		fw.dropped.Add(1)
	}
	return len(p), nil
}

// Dropped は転送できなかった行数（キュー満杯または送信エラー）を返す。
// テストで使う。本番の呼び出し側は不要だ。
// Dropped reports how many lines could not be forwarded (queue full or send
// error). It is used by tests; production callers do not need it.
func (fw *forwardWriter) Dropped() uint64 { return fw.dropped.Load() }

// Close は送信側を停止し、送信中の 1 行を（有界に）待つ。
// Close stops the sender and waits (bounded) for the in-flight line.
func (fw *forwardWriter) Close() error {
	fw.closeOnce.Do(func() {
		// 書き込みロックを取り、チャネルを閉じている間に Write が送信中にならないようにする
		// （sendMu 参照）。ch を閉じる前に closed を設定し、これが返った後に読み取りロックを
		// 取る Write が closed==true を見るようにする。
		// Take the write lock so no Write is mid-send while we close the
		// channel (see sendMu). Set closed before closing ch so a Write that
		// acquires the read lock after this returns sees closed==true.
		fw.sendMu.Lock()
		fw.closed.Store(true)
		close(fw.ch)
		fw.sendMu.Unlock()
	})
	select {
	case <-fw.done:
	case <-time.After(forwardCloseGrace):
	}
	return nil
}
