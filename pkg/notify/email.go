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

	// Upgrade with STARTTLS when the server supports it (matches the old
	// smtp.SendMail behaviour, which negotiated STARTTLS automatically).
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: e.host}); err != nil {
			return fmt.Errorf("email starttls: %w", err)
		}
	}

	if e.username != "" {
		auth := smtp.PlainAuth("", e.username, e.password, e.host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("email auth: %w", err)
		}
	}

	if err := c.Mail(e.from); err != nil {
		return fmt.Errorf("email mail: %w", err)
	}
	for _, rcpt := range e.to {
		if err := c.Rcpt(rcpt); err != nil {
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
