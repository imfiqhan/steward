package steward

import (
	"context"
	"fmt"
	"net/smtp"
	"strings"
	"time"
)

// SMTPMailer sends mail through a plain SMTP endpoint (STARTTLS when the
// server offers it, via net/smtp's default behavior).
type SMTPMailer struct {
	Host     string // "smtp.example.com"
	Port     int    // 587
	Username string
	Password string
	From     string // "Steward <admin@example.com>"
}

// Send implements Mailer.
func (m *SMTPMailer) Send(_ context.Context, mail Mail) error {
	if m.Host == "" || m.From == "" {
		return fmt.Errorf("steward: SMTPMailer needs Host and From")
	}
	port := m.Port
	if port == 0 {
		port = 587
	}
	var auth smtp.Auth
	if m.Username != "" {
		auth = smtp.PlainAuth("", m.Username, m.Password, m.Host)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", m.From)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(mail.To, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", mail.Subject)
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "MIME-Version: 1.0\r\n")
	if mail.HTML != "" {
		fmt.Fprintf(&b, "Content-Type: text/html; charset=utf-8\r\n\r\n%s", mail.HTML)
	} else {
		fmt.Fprintf(&b, "Content-Type: text/plain; charset=utf-8\r\n\r\n%s", mail.Text)
	}
	addr := fmt.Sprintf("%s:%d", m.Host, port)
	from := m.From
	if i := strings.LastIndexByte(from, '<'); i >= 0 {
		from = strings.TrimRight(from[i+1:], ">")
	}
	return smtp.SendMail(addr, auth, from, mail.To, []byte(b.String()))
}
