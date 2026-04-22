// Package email 提供邮件发送能力。
//
// Sender 是唯一对外接口,当前实现 configSender 同时支持两种通道:
//   - "resend":走 Resend HTTP API(github.com/resend/resend-go/v3)
//   - "smtp"  :走 SMTP + TLS(crypto/tls + net/smtp)
//
// Provider 为空串 = no-op(dev 本地:码只写 Redis,看日志即可)。
// 失败返回 sentinel ErrSend,上层可选是否把"发码失败"当致命错误。
package email

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"

	"github.com/eyrihe999-stack/Synapse/config"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/resend/resend-go/v3"
)

// ErrSend 邮件发送失败的 sentinel。上层 wrap 后用 errors.Is 判定。
//sayso-lint:ignore sentinel-comment
var ErrSend = errors.New("email: send failed")

// ErrProviderDisabled 未配置 provider(provider="")。调用方可据此降级(仅写 Redis)。
//sayso-lint:ignore sentinel-comment
var ErrProviderDisabled = errors.New("email: provider disabled")

// Sender 邮件发送接口。
type Sender interface {
	// SendVerificationEmail 发送 HTML 邮件。
	// provider="" 时返回 ErrProviderDisabled,由调用方决定是否当错误。
	SendVerificationEmail(ctx context.Context, to, subject, htmlBody string) error
}

type configSender struct {
	cfg *config.EmailConfig
	log logger.LoggerInterface
}

// NewSender 根据配置构造 Sender。
func NewSender(cfg *config.EmailConfig, log logger.LoggerInterface) Sender {
	return &configSender{cfg: cfg, log: log}
}

func (s *configSender) SendVerificationEmail(ctx context.Context, to, subject, htmlBody string) error {
	switch s.cfg.Provider {
	case "resend":
		return s.sendViaResend(ctx, to, subject, htmlBody)
	case "smtp":
		return s.sendViaSMTP(ctx, to, subject, htmlBody)
	case "":
		return ErrProviderDisabled
	default:
		s.log.WarnCtx(ctx, "unknown email provider", map[string]any{"provider": s.cfg.Provider, "to": to})
		return fmt.Errorf("unknown provider %q: %w", s.cfg.Provider, ErrSend)
	}
}

func (s *configSender) sendViaResend(ctx context.Context, to, subject, body string) error {
	if s.cfg.APIKey == "" {
		s.log.ErrorCtx(ctx, "Resend API key 未配置", nil, map[string]any{"to": to})
		return fmt.Errorf("resend api key missing: %w", ErrSend)
	}

	from := s.formatFrom()
	client := resend.NewClient(s.cfg.APIKey)
	sent, err := client.Emails.Send(&resend.SendEmailRequest{
		From:    from,
		To:      []string{to},
		Subject: subject,
		Html:    body,
	})
	if err != nil {
		s.log.ErrorCtx(ctx, "Resend 发送失败", err, map[string]any{"to": to})
		return fmt.Errorf("resend send: %w: %w", err, ErrSend)
	}
	s.log.InfoCtx(ctx, "邮件已通过 Resend 发送", map[string]any{"to": to, "email_id": sent.Id})
	return nil
}

func (s *configSender) sendViaSMTP(ctx context.Context, to, subject, body string) error {
	if s.cfg.SMTPHost == "" || s.cfg.From == "" || s.cfg.Password == "" {
		s.log.ErrorCtx(ctx, "SMTP 配置不完整", nil, map[string]any{"to": to})
		return fmt.Errorf("smtp config incomplete: %w", ErrSend)
	}

	fromAddr := s.formatFrom()
	var msg bytes.Buffer
	fmt.Fprintf(&msg, "From: %s\r\n", fromAddr)
	fmt.Fprintf(&msg, "To: %s\r\n", to)
	fmt.Fprintf(&msg, "Subject: %s\r\n", subject)
	fmt.Fprintf(&msg, "Content-Type: text/html; charset=UTF-8\r\n")
	fmt.Fprintf(&msg, "\r\n%s", body)

	addr := fmt.Sprintf("%s:%d", s.cfg.SMTPHost, s.cfg.SMTPPort)
	username := s.cfg.Username
	if username == "" {
		username = s.cfg.From
	}
	auth := smtp.PlainAuth("", username, s.cfg.Password, s.cfg.SMTPHost)

	conn, err := tls.Dial("tcp", addr, nil)
	if err != nil {
		s.log.ErrorCtx(ctx, "SMTP TLS 连接失败", err, map[string]any{"to": to, "addr": addr})
		return fmt.Errorf("tls dial: %w: %w", err, ErrSend)
	}
	//sayso-lint:ignore err-swallow
	host, _, _ := net.SplitHostPort(addr)
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		//sayso-lint:ignore err-swallow
		_ = conn.Close()
		s.log.ErrorCtx(ctx, "SMTP 客户端创建失败", err, map[string]any{"to": to})
		return fmt.Errorf("smtp new client: %w: %w", err, ErrSend)
	}
	//sayso-lint:ignore err-swallow
	defer func() { _ = c.Close() }()

	if ok, _ := c.Extension("AUTH"); ok {
		if err := c.Auth(auth); err != nil {
			s.log.ErrorCtx(ctx, "SMTP 认证失败", err, map[string]any{"to": to})
			return fmt.Errorf("smtp auth: %w: %w", err, ErrSend)
		}
	}
	if err := c.Mail(s.cfg.From); err != nil {
		return fmt.Errorf("smtp mail: %w: %w", err, ErrSend)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt: %w: %w", err, ErrSend)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w: %w", err, ErrSend)
	}
	//sayso-lint:ignore err-swallow
	if _, err := w.Write(msg.Bytes()); err != nil {
		return fmt.Errorf("smtp write: %w: %w", err, ErrSend)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close data: %w: %w", err, ErrSend)
	}
	s.log.InfoCtx(ctx, "邮件已通过 SMTP 发送", map[string]any{"to": to})
	if err := c.Quit(); err != nil {
		return fmt.Errorf("smtp quit: %w: %w", err, ErrSend)
	}
	return nil
}

func (s *configSender) formatFrom() string {
	if s.cfg.FromName == "" {
		return s.cfg.From
	}
	return fmt.Sprintf("%s <%s>", s.cfg.FromName, s.cfg.From)
}
