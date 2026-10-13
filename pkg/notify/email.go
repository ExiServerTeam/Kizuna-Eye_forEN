package notify

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// ============================================================
// EmailNotifier は SMTP で通知を送る。
// EmailNotifier sends notifications via SMTP.
// ============================================================
type EmailNotifier struct {
	host     string
	port     string
	username string
	password string
	from     string
	to       []string
}

// NewEmailNotifier は EmailNotifier を作る。
// to はカンマ区切りの宛先リストだ。
// NewEmailNotifier creates an EmailNotifier.
// to is a comma-separated recipient list.
func NewEmailNotifier(host, port, username, password, from, to string) *EmailNotifier {
	var recipients []string
	for _, addr := range strings.Split(to, ",") {
		addr = strings.TrimSpace(addr)
		if addr != "" {
			recipients = append(recipients, addr)
		}
	}
	if port == "" {
		port = "587"
	}
	if from == "" {
		from = username
	}
	return &EmailNotifier{
		host:     host,
		port:     port,
		username: username,
		password: password,
		from:     from,
		to:       recipients,
	}
}

func (e *EmailNotifier) Name() string { return "email" }

// sanitizeHeader は CR と LF を除去し、値が余分なメールヘッダを注入できないようにする
// （ヘッダインジェクション）。
// sanitizeHeader removes CR and LF so a value cannot inject extra email
// headers (header injection).
func sanitizeHeader(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	return s
}

func (e *EmailNotifier) Send(ctx context.Context, a *Alert) error {
	if e.host == "" || len(e.to) == 0 {
		return fmt.Errorf("email: host または宛先が未設定です")
	}

	subject := fmt.Sprintf("[Kizuna-Eye] %s%s", a.IconPrefix(), a.Title)
	body := a.FullMessage() + "\n\n" + a.Timestamp.Format(time.RFC3339)

	// すべてのヘッダ値から CR/LF を除去し、ヘッダインジェクションを防ぐ。
	// アラートタイトルは解析済みログ行に由来し得るため、信頼できない。
	// Strip CR/LF from every header value to prevent header injection. Alert
	// titles can originate from parsed log lines, so they are untrusted.
	headers := []string{
		"From: " + sanitizeHeader(e.from),
		"To: " + sanitizeHeader(strings.Join(e.to, ", ")),
		"Subject: " + sanitizeHeader(subject),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"Date: " + time.Now().Format(time.RFC1123Z),
	}
	msg := []byte(strings.Join(headers, "\r\n") + "\r\n\r\n" + body + "\r\n")

	addr := e.host + ":" + e.port

	// smtp.SendMail は context を無視しタイムアウトも無いため、SMTP サーバーが
	// ハングするとこのゴルーチンが永久にブロックし、やがて notify マネージャの
	// セマフォを枯渇させる。ctx（とその期限）を効かせるため手動で dial する。
	// smtp.SendMail ignores context and has no timeout, so a hung SMTP server
	// would block this goroutine forever and eventually exhaust the notify
	// manager's semaphore. Dial manually so ctx (and its deadline) applies.
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("email dial: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	c, err := smtp.NewClient(conn, e.host)
	if err != nil {
		return fmt.Errorf("email client: %w", err)
	}
	defer c.Close()

	// STARTTLS でアップグレードする。これは FAIL-CLOSED だ。サーバーが
	// STARTTLS を広告しない場合、続行せず拒否する。
	//
	// smtp.PlainAuth は接続がローカルでない限り暗号化されていない接続で
	// パスワードを送るのを拒否するが、静かなダウングレードはすべての通知を
	// 落とすか、（より悪いことに非 PlainAuth 機構で）資格情報を漏らす。
	// ここで失敗させると、設定ミスが静かなセキュリティダウングレードではなく
	// 可視の配送エラーになる。以前のコードは「サーバーが対応していれば」
	// アップグレードするだけだった。
	// Upgrade with STARTTLS. This is FAIL-CLOSED: if the server does not
	// advertise STARTTLS we refuse to continue rather than fall through.
	//
	// smtp.PlainAuth refuses to send the password over an unencrypted
	// connection, but only if the connection is not already local. A silent
	// downgrade would therefore either drop every notification or (worse,
	// with a non-PlainAuth mechanism) leak credentials. Failing here makes the
	// misconfiguration a visible delivery error instead of a silent security
	// downgrade. The old code only upgraded "when the server supports it".
	if ok, _ := c.Extension("STARTTLS"); !ok {
		return fmt.Errorf("email: SMTP サーバーが STARTTLS に対応していません（暗号化されないため送信を中止しました）。smtps(465) を使ってください")
	}
	if err := c.StartTLS(&tls.Config{ServerName: e.host, MinVersion: tls.VersionTLS12}); err != nil {
		return fmt.Errorf("email starttls: %w", err)
	}

	if e.username != "" {
		auth := smtp.PlainAuth("", e.username, e.password, e.host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("email auth: %w", err)
		}
	}

	// エンベロープアドレスも無害化する。上のヘッダブロックは既に CR/LF を
	// 除去しているが、c.Mail / c.Rcpt は生の値を SMTP コマンド行へ書き込む。
	// （設定由来の）アドレスに埋め込まれた CRLF が余分な SMTP コマンドを
	// 注入し得る。多層防御だ。アドレスは admin が設定するが、どの経路でも
	// ヘッダ/コマンドインジェクションを許してはならない。
	// Sanitize the envelope addresses too. The header block above already
	// strips CR/LF, but c.Mail / c.Rcpt write the raw value into the SMTP
	// command line; an embedded CRLF in a (config-supplied) address could
	// otherwise inject extra SMTP commands. Defense in depth: the addresses
	// are admin-configured, but no path should allow header/command injection.
	if err := c.Mail(sanitizeHeader(e.from)); err != nil {
		return fmt.Errorf("email mail: %w", err)
	}
	for _, rcpt := range e.to {
		if err := c.Rcpt(sanitizeHeader(rcpt)); err != nil {
			return fmt.Errorf("email rcpt: %w", err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("email data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("email write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("email close: %w", err)
	}
	return c.Quit()
}
