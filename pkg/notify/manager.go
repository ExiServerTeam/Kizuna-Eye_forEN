package notify

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"Kizuna-Eye/pkg/config"
	"Kizuna-Eye/pkg/logger"
)

// maxConcurrentSends は同時実行中の通知ゴルーチン数を制限し、アラートの嵐が
// 無制限のゴルーチンを生み出さないようにする。
// maxConcurrentSends bounds the number of in-flight notification goroutines
// so an alert storm cannot spawn an unbounded number of them.
const maxConcurrentSends = 32

// criticalSendWait はマネージャが飽和したとき、critical アラートが空きスロットを
// 待つ上限だ。非 critical アラートは代わりに破棄され、低優先のノイズのバーストが
// critical 通知を枯渇させることが決してないようにする。
// criticalSendWait bounds how long a critical alert waits for a free send slot
// when the manager is saturated. Non-critical alerts are dropped instead, so a
// burst of low-priority noise can never starve a critical notification.
const criticalSendWait = 5 * time.Second

// batchMaxItems は 1 つの集約メッセージの上限だ。洪水が巨大な Discord embed を
// 構築しないようにする（どうせ拒否される）。
// batchMaxItems bounds one batched message so a flood cannot build an
// enormous Discord embed (which would be rejected anyway).
const batchMaxItems = 50

// legacySendTimeout はリトライ無効時に送信の元の 15 秒予算を保つ。これにより
// リトライキーを決して設定しないインストールは以前と全く同じに動作する。
// legacySendTimeout keeps the original 15s budget for a send when retries are
// disabled, so an installation that never sets the retry keys behaves exactly
// as before.
const legacySendTimeout = 15 * time.Second

// maxSendTimeout は大きなリトライ/バックオフ設定でも送信ごとの予算を制限し、
// 到達不能な webhook 1 つが送信スロットを永久に占有しないようにする。
// maxSendTimeout caps the per-send budget even with a large retry/backoff
// configuration, so one unreachable webhook cannot hold a send slot forever.
const maxSendTimeout = 2 * time.Minute

// sendTimeoutFor は 1 回の Send が取り得る時間を返す。legacySendTimeout に
// 指数バックオフ待機の最悪ケース合計（1s、2s、4s、... で backoffMax 上限）を足す。
// これが無いと 15 秒の context がリトライループをバックオフ中にキャンセルし、
// リトライすれば成功したはずのアラートが失われる。
// sendTimeoutFor returns how long a single Send may take: legacySendTimeout
// plus the worst-case sum of the exponential backoff waits (1s, 2s, 4s, ...
// capped at backoffMax). Without this, the 15s context cancels the retry loop
// mid-backoff and the alert is lost even though retrying would have worked.
func sendTimeoutFor(retries int, backoffMax time.Duration) time.Duration {
	if retries <= 0 {
		return legacySendTimeout
	}
	total := legacySendTimeout
	wait := time.Second
	for i := 0; i < retries; i++ {
		if wait > backoffMax {
			wait = backoffMax
		}
		total += wait
		wait *= 2
	}
	if total > maxSendTimeout {
		total = maxSendTimeout
	}
	return total
}

// ============================================================
// Manager は通知を複数チャンネルへファンアウトする。
// Manager fans out notifications to multiple channels.
// ============================================================
type Manager struct {
	notifiers []Notifier
	log       *logger.Logger
	enabled   bool
	sem       chan struct{}

	// --- バッチ集約 (タスク1) ---
	// batchEnabled は batchWindow 内に到着したアラートを 1 メッセージに集約する。
	// 履歴は呼び出し側がアラート単位で記録し、集約するのは通知だけだ。
	// batchEnabled groups alerts that arrive within batchWindow into one
	// message. History is recorded per-alert by the caller; only the
	// notification is aggregated.
	batchEnabled         bool
	batchWindow          time.Duration
	batchExcludeCritical bool

	batchMu    sync.Mutex
	batch      []*Alert
	batchTimer *time.Timer

	// sendTimeout は送信ごとの context 予算だ（sendTimeoutFor 参照）。
	// sendTimeout is the per-send context budget (see sendTimeoutFor).
	sendTimeout time.Duration
}

// NewManager は Manager を作る。
// NewManager creates a Manager.
func NewManager(log *logger.Logger) *Manager {
	return &Manager{
		notifiers:   make([]Notifier, 0),
		log:         log,
		enabled:     true,
		sem:         make(chan struct{}, maxConcurrentSends),
		sendTimeout: legacySendTimeout,
	}
}

// FromConfig は config.NotificationsConfig から Manager を組み立てる。
// FromConfig builds a Manager from a config.NotificationsConfig.
func FromConfig(cfg config.NotificationsConfig, log *logger.Logger) *Manager {
	m := NewManager(log)

	// バッチ設定（既定は計画と一致: 有効、5 秒窓、critical 除外）。
	// *bool: キー未設定は既定（true）を意味する。
	// Batch settings (defaults match the plan: enabled, 5s window, exclude critical).
	// *bool: absent key means the default (true).
	m.batchEnabled = cfg.BatchEnabled == nil || *cfg.BatchEnabled
	m.batchWindow = time.Duration(cfg.BatchWindowSec) * time.Second
	if m.batchWindow <= 0 {
		m.batchWindow = 5 * time.Second
	}
	// *bool: キー未設定は既定 true を意味する。
	// *bool: absent key means default true.
	m.batchExcludeCritical = cfg.BatchExcludeCritical == nil || *cfg.BatchExcludeCritical

	if !cfg.Enabled {
		m.enabled = false
		if log != nil {
			log.InfoT("notify.disabled")
		}
		return m
	}

	// *int: キー未設定は既定（5）を意味する。単なる int として読むと
	// "キー未設定" が 0 になり、すべてのリトライを黙って無効化していた。
	// *int: an absent key means the default (5). Reading it as a plain int
	// turned "key not set" into 0, which silently disabled every retry.
	retries := defaultDiscordMaxRetries
	if cfg.DiscordMaxRetries != nil {
		retries = *cfg.DiscordMaxRetries
		if retries < 0 {
			retries = 0
		}
	}
	backoffMax := cfg.DiscordBackoffMaxSec
	if backoffMax <= 0 {
		backoffMax = defaultBackoffMaxSec
	}
	// 送信ごとの予算をリトライ設定と同期させ、context が実行するよう設定された
	// バックオフをキャンセルしないようにする。
	// Keep the per-send budget in step with the retry settings, so the
	// context does not cancel the backoff it was configured to perform.
	m.sendTimeout = sendTimeoutFor(retries, time.Duration(backoffMax)*time.Second)

	for _, ch := range cfg.Channels {
		if !ch.Enabled {
			continue
		}
		// "Discord" や " discord " も "discord" と同様に受け付け、余分な空白や
		// 大文字がチャンネルを黙って落とさないようにする。
		// Accept "Discord" / " discord " as well as "discord" so a stray
		// space or capital does not silently drop the channel.
		switch strings.ToLower(strings.TrimSpace(ch.Type)) {
		case "discord":
			// 同梱のサンプル URL のままのチャンネルは決して成功し得ない。
			// Discord は 400 を返し、リトライループが永久にノイズを出す。
			// 失敗が保証された notifier を同梱するのではなく、スキップする
			// （設定ローダーが警告する）。
			// A channel left at the shipped sample URL can never succeed:
			// Discord answers 400 and the retry loop makes noise forever.
			// Skip it (the config loader warns) instead of shipping a
			// notifier that is guaranteed to fail.
			if config.IsPlaceholderWebhookURL(ch.WebhookURL) {
				if log != nil {
					log.WarnT("notify.placeholder_skipped", "discord")
				}
				continue
			}
			m.notifiers = append(m.notifiers, NewDiscordNotifierWithOptions(ch.WebhookURL, retries, backoffMax, loggerAdapter{log}))
			if log != nil {
				log.InfoT("notify.discord_reg", retries, backoffMax)
			}
		case "telegram":
			m.notifiers = append(m.notifiers, NewTelegramNotifier(ch.BotToken, ch.ChatID))
			if log != nil {
				log.InfoT("notify.telegram_reg")
			}
		case "line":
			m.notifiers = append(m.notifiers, NewLINENotifier(ch.Token))
			if log != nil {
				log.InfoT("notify.line_reg")
			}
		case "slack":
			if config.IsPlaceholderWebhookURL(ch.WebhookURL) {
				if log != nil {
					log.WarnT("notify.placeholder_skipped", "slack")
				}
				continue
			}
			m.notifiers = append(m.notifiers, NewSlackNotifier(ch.WebhookURL))
			if log != nil {
				log.InfoT("notify.slack_reg")
			}
		case "email":
			m.notifiers = append(m.notifiers, NewEmailNotifier(
				ch.SMTPHost, ch.SMTPPort, ch.SMTPUsername, ch.SMTPPassword, ch.EmailFrom, ch.EmailTo,
			))
			if log != nil {
				log.InfoT("notify.email_reg")
			}
		}
	}

	if log != nil {
		log.InfoT("notify.channel_count", len(m.notifiers))
		if m.batchEnabled {
			log.InfoT("notify.batch_enabled", m.batchWindow, m.batchExcludeCritical)
		}
	}
	return m
}

// loggerAdapter は *logger.Logger を notify.Logger インターフェースへ橋渡しする。
// loggerAdapter bridges *logger.Logger to the notify.Logger interface.
type loggerAdapter struct{ log *logger.Logger }

func (a loggerAdapter) Info(f string, args ...interface{}) {
	if a.log != nil {
		a.log.Info(f, args...)
	}
}
func (a loggerAdapter) Warn(f string, args ...interface{}) {
	if a.log != nil {
		a.log.Warn(f, args...)
	}
}
func (a loggerAdapter) Error(f string, args ...interface{}) {
	if a.log != nil {
		a.log.Error(f, args...)
	}
}
func (a loggerAdapter) Debug(f string, args ...interface{}) {
	if a.log != nil {
		a.log.Debug(f, args...)
	}
}

// Notify はアラートを記録し、全チャンネルへ配送する。バッチが有効な場合、
// 非 critical アラートは batchWindow の間保持され 1 メッセージとして送られる。
// critical アラート（batchExcludeCritical 設定時）は即座に出る。
// Notify records the alert and dispatches it to all channels. When batching
// is enabled, non-critical alerts are held for batchWindow and sent as one
// message; critical alerts (when batchExcludeCritical is set) go out at once.
func (m *Manager) Notify(a *Alert) {
	if a == nil {
		return
	}
	if !m.enabled || len(m.notifiers) == 0 {
		return
	}

	if m.log != nil {
		m.log.InfoT("notify.send", a.Level, a.Message)
	}

	// バッチ経路: 非 critical（除外されていなければ critical も）アラートを保持する。
	// Batching path: hold non-critical (and critical, if not excluded) alerts.
	if m.batchEnabled {
		isCritical := a.Level == LevelCritical
		if !(isCritical && m.batchExcludeCritical) {
			m.addToBatch(a)
			return
		}
	}

	m.dispatch(a)
}

// addToBatch は現在の窓にアラートを追加し、フラッシュを予約する。
// addToBatch appends an alert to the current window and schedules the flush.
func (m *Manager) addToBatch(a *Alert) {
	m.batchMu.Lock()
	m.batch = append(m.batch, a)
	if m.batchTimer == nil {
		m.batchTimer = time.AfterFunc(m.batchWindow, m.flushBatch)
	}
	m.batchMu.Unlock()
}

// flushBatch は蓄積したアラートを 1 メッセージとして送り、窓をリセットする。
// flushBatch sends the accumulated alerts as one message and resets the window.
func (m *Manager) flushBatch() {
	m.batchMu.Lock()
	items := m.batch
	m.batch = nil
	m.batchTimer = nil
	m.batchMu.Unlock()

	if len(items) == 0 {
		return
	}
	if len(items) == 1 {
		m.dispatch(items[0])
		return
	}
	m.dispatch(m.buildBatchAlert(items))
}

// buildBatchAlert は N 件のアラートを 1 件の要約アラートへ畳み込む。個々の
// アラートは履歴に残る（呼び出し側が各々を記録する）。集約するのは通知だけだ。
// buildBatchAlert collapses N alerts into one summary alert. The individual
// alerts remain in the history (the caller records each one); only the
// notification is aggregated.
func (m *Manager) buildBatchAlert(items []*Alert) *Alert {
	n := len(items)
	shown := n
	if shown > batchMaxItems {
		shown = batchMaxItems
	}

	// 最高レベルが勝ち、集約が過小評価されないようにする。
	// Highest level wins so the batch is not understated.
	level := LevelInfo
	for _, a := range items {
		if levelWeight(a.Level) > levelWeight(level) {
			level = a.Level
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d 件のアラート（%s 間に集約）\n", n, m.batchWindow)
	for i := 0; i < shown; i++ {
		a := items[i]
		fmt.Fprintf(&b, "• [%s] %s\n", a.Level, a.FullTitle())
	}
	if n > shown {
		fmt.Fprintf(&b, "… 他 %d 件（履歴には個別に記録されています）\n", n-shown)
	}

	// Timestamp は最新アイテムにし、集約が最新の瞬間に現れるようにする。
	// Timestamp = newest item so the batch appears at the latest moment.
	ts := items[0].Timestamp
	for _, a := range items {
		if a.Timestamp.After(ts) {
			ts = a.Timestamp
		}
	}

	return &Alert{
		Type:      "batch",
		Level:     level,
		Icon:      items[0].Icon,
		Title:     fmt.Sprintf("アラート集約 (%d件)", n),
		Message:   b.String(),
		Timestamp: ts,
	}
}

// levelWeight はレベルを順位付けし、集約が最高のものを報告するようにする。
// levelWeight ranks levels so the batch reports the highest one.
func levelWeight(l Level) int {
	switch l {
	case LevelCritical:
		return 3
	case LevelWarning:
		return 2
	default:
		return 1
	}
}

// dispatch は 1 件のアラートを全 notifier へ送り、同時実行上限を守る。
// dispatch sends one alert to every notifier, honoring the concurrency cap.
func (m *Manager) dispatch(a *Alert) {
	isCritical := a.Level == LevelCritical
	for _, n := range m.notifiers {
		select {
		case m.sem <- struct{}{}:
			m.sendAsync(n, a)
		default:
			// 飽和。critical アラートはスロットを（有界に）待つ価値があり、
			// 失われない。非 critical アラートはアラートの嵐からシステムを
			// 守るため破棄する。
			// Saturated. A critical alert is worth waiting for a slot (bounded),
			// so it is not lost. Non-critical alerts are dropped to protect the
			// system from an alert storm.
			if !isCritical {
				if m.log != nil {
					m.log.WarnT("notify.concurrent_skip", maxConcurrentSends, n.Name())
				}
				continue
			}
			timer := time.NewTimer(criticalSendWait)
			select {
			case m.sem <- struct{}{}:
				timer.Stop()
				m.sendAsync(n, a)
			case <-timer.C:
				if m.log != nil {
					m.log.WarnT("notify.timeout_skip", n.Name())
				}
			}
		}
	}
}

// sendAsync は 1 つの notifier を専用ゴルーチンで実行し、完了まで送信スロットを
// 保持する。
// sendAsync runs one notifier in its own goroutine, holding a send slot until
// it finishes.
func (m *Manager) sendAsync(notifier Notifier, a *Alert) {
	go func() {
		defer func() { <-m.sem }()
		// recover し、notifier の Send 内の panic がプロセス全体（すべての監視を
		// 止める）を落とさないようにする。スロットは上の defer の受信で依然解放される。
		// Recover so a panic inside a notifier's Send cannot crash the whole
		// process (which would stop all monitoring). The slot is still
		// released by the deferred receive above.
		defer func() {
			if r := recover(); r != nil {
				if m.log != nil {
					m.log.ErrorT("notify.panic", notifier.Name(), r)
				}
			}
		}()
		timeout := m.sendTimeout
		if timeout <= 0 {
			timeout = legacySendTimeout
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := notifier.Send(ctx, a); err != nil {
			if m.log != nil {
				m.log.WarnT("notify.failed", notifier.Name(), err)
			}
			return
		}
		if m.log != nil {
			m.log.DebugT("notify.success", notifier.Name())
		}
	}()
}

// HasChannels は少なくとも 1 つのチャンネルが有効かを返す。
// HasChannels reports whether at least one channel is enabled.
func (m *Manager) HasChannels() bool {
	return m.enabled && len(m.notifiers) > 0
}

// HasChannel は指定名の notifier が登録されているかを返す。
// HasChannel reports whether a notifier with the given name is registered.
func (m *Manager) HasChannel(name string) bool {
	for _, n := range m.notifiers {
		if n.Name() == name {
			return true
		}
	}
	return false
}

// ChannelNames は登録済み notifier の名前を返す。
// ChannelNames returns the names of the registered notifiers.
func (m *Manager) ChannelNames() []string {
	out := make([]string, 0, len(m.notifiers))
	for _, n := range m.notifiers {
		out = append(out, n.Name())
	}
	return out
}

// SendToChannels はアラートを指定名のチャンネル（例: "email"）にのみ送り、
// バッチと通常のファンアウトを迂回する。Discord/Telegram をもスパムすべきでない
// 低頻度の運用レポートに使う。未知の名前は無視する。アラートはアラート履歴に
// 記録されない（呼び出し側が決める。レポートはアラートではない）。
// SendToChannels sends an alert only to the named channels (e.g. "email"),
// bypassing batching and the normal fan-out. It is used for low-frequency
// operational reports that should not also spam Discord/Telegram. An unknown
// name is ignored. The alert is NOT recorded in the alert history (the caller
// decides; a report is not an alert).
func (m *Manager) SendToChannels(a *Alert, names ...string) {
	if a == nil || !m.enabled {
		return
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	for _, notifier := range m.notifiers {
		if !want[notifier.Name()] {
			continue
		}
		select {
		case m.sem <- struct{}{}:
			m.sendAsync(notifier, a)
		default:
			if m.log != nil {
				m.log.WarnT("notify.concurrent_skip", maxConcurrentSends, notifier.Name())
			}
		}
	}
}

// Flush は保留中の集約アラートを即座に送る。保留中の窓を失わないよう
// シャットダウン時に呼ぶ。
// Flush sends any pending batched alerts immediately. Call on shutdown so a
// pending window is not lost.
func (m *Manager) Flush() {
	m.batchMu.Lock()
	if m.batchTimer != nil {
		m.batchTimer.Stop()
	}
	m.batchMu.Unlock()
	m.flushBatch()
}
