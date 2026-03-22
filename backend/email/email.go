package email

import (
	"fmt"
	"net/smtp"
	"strings"
)

type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
}

func SendInviteEmail(cfg SMTPConfig, toEmail, inviteURL, orgName, inviterName string) error {
	subject := fmt.Sprintf("You've been invited to %s on HLL RADAR", orgName)

	body := fmt.Sprintf(`<!DOCTYPE html>
<html>
<body style="font-family: sans-serif; max-width: 600px; margin: 0 auto;">
  <h2>You've been invited to HLL RADAR</h2>
  <p><strong>%s</strong> has invited you to join <strong>%s</strong> on HLL RADAR.</p>
  <p>Click the link below to accept the invitation and create your account:</p>
  <p><a href="%s" style="display: inline-block; padding: 12px 24px; background: #2563eb; color: white; text-decoration: none; border-radius: 6px;">Accept Invitation</a></p>
  <p style="color: #666; font-size: 14px;">Or copy this link: %s</p>
  <p style="color: #999; font-size: 12px;">This invitation expires in 7 days.</p>
</body>
</html>`, inviterName, orgName, inviteURL, inviteURL)

	msg := buildMIMEMessage(cfg.From, toEmail, subject, body)

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)

	var a smtp.Auth
	if cfg.Username != "" {
		a = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	}

	if err := smtp.SendMail(addr, a, cfg.From, []string{toEmail}, []byte(msg)); err != nil {
		return fmt.Errorf("failed to send invite email to %s: %w", toEmail, err)
	}

	return nil
}

func buildMIMEMessage(from, to, subject, htmlBody string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("From: %s\r\n", from))
	b.WriteString(fmt.Sprintf("To: %s\r\n", to))
	b.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
	b.WriteString("\r\n")
	b.WriteString(htmlBody)
	return b.String()
}
