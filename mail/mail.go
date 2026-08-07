// Package mail sends mail through the configured SMTP server.
//
// It exists because the SMTP settings shipped before anything used them: the
// config keys, the flags and a whole Settings section were configurable, and no
// code path ever opened a connection. This is the consumer — currently one test
// message, so an admin can find out whether what they typed works before anything
// depends on it.
//
// The transport is stdlib `net/smtp`. Encryption follows `smtp_tls`
// (models.SMTPTLS*): the default `auto` infers it from the port — 465 is implicit
// TLS, everything else is upgraded with STARTTLS when the server advertises it — and
// the explicit modes exist for a relay that gets that wrong. Auth is PLAIN, and only
// when a username is configured: a local relay that accepts unauthenticated mail on
// 25 is a normal deployment, and demanding credentials for it would be inventing a
// requirement.
//
// One rule overrides everything else: on a test instance every message goes to the
// test recipient. See redirectInTestEnvironment.
package mail

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
)

// implicitTLSPort is the historical SMTPS port, where TLS wraps the connection from
// the first byte rather than being negotiated with STARTTLS.
const implicitTLSPort = 465

// dialTimeout bounds the connect: a wrong host in the settings must fail as a
// message on the page, not as a request that hangs until the browser gives up.
const dialTimeout = 15 * time.Second

// send is the seam the tests replace. It carries everything the transport needs so
// a test can assert what would have gone over the wire without a server.
var send = sendSMTP

// Configured reports whether enough is set for a send to be attempted. It is the
// same question the external clients answer before they are constructed: mail is
// optional, and "not configured" is not an error until someone asks for a send.
func Configured(config models.ConfigStruct) bool {
	return config.SMTPEnabled && config.SMTPHost != "" && config.SMTPFrom != "" && config.SMTPPort > 0
}

// Send delivers one plain-text message to every recipient.
//
// On a test instance it delivers to the test recipient instead — see
// redirectInTestEnvironment. That rule has no exception and no opt-out, which is why
// it lives here, at the one point every message passes through, rather than in the
// callers that would each have to remember it.
func Send(config models.ConfigStruct, to []string, subject, body string) error {
	recipients := make([]string, 0, len(to))
	for _, address := range to {
		if address = strings.TrimSpace(address); address != "" {
			recipients = append(recipients, address)
		}
	}
	if len(recipients) == 0 {
		return errors.New("no recipient was given")
	}
	if !config.SMTPEnabled {
		return errors.New("email is disabled in the settings")
	}
	if config.SMTPHost == "" {
		return errors.New("no SMTP host is configured")
	}
	if config.SMTPPort <= 0 {
		return errors.New("no SMTP port is configured")
	}
	if config.SMTPFrom == "" {
		return errors.New("no from address is configured")
	}

	recipients, err := redirectInTestEnvironment(config, recipients)
	if err != nil {
		return err
	}

	message := buildMessage(config.SMTPFrom, recipients, subject, body, time.Now())
	return send(config, recipients, message)
}

// redirectInTestEnvironment forces every message on a test instance to the test
// recipient.
//
// A test instance is one pointed at a copy of the real database — the same users, the
// same addresses — so the cost of getting this wrong is mailing real people from a
// scratch deployment. The redirect is therefore unconditional: it ignores the
// caller's recipients, and it has no override, because an override is the thing that
// gets used by accident.
//
// With no test recipient configured there is nowhere safe to send, so the send is
// refused rather than quietly falling back to the real address.
func redirectInTestEnvironment(config models.ConfigStruct, recipients []string) ([]string, error) {
	if !strings.EqualFold(strings.TrimSpace(config.AutotaggerrEnvironment), "test") {
		return recipients, nil
	}

	testRecipient := strings.TrimSpace(config.AutotaggerrTestEmail)
	if testRecipient == "" {
		return nil, errors.New("this instance is set to the test environment and no test recipient is configured; refusing to send")
	}
	if len(recipients) != 1 || !strings.EqualFold(recipients[0], testRecipient) {
		logger.Log.Warnf("test environment: redirecting mail for %s to %s",
			strings.Join(recipients, ", "), testRecipient)
	}
	return []string{testRecipient}, nil
}

// SendTest sends the "your SMTP settings work" message to the configured test
// recipient, or to override when one is given. It is what the Settings page's
// button calls, and the only caller of Send today. The recipient is returned so the
// page can name it: "sent" is not an answer if the admin cannot see where to.
//
// On a test instance the override is ignored along with everything else — Send
// redirects to the test recipient, and the returned address is corrected to match so
// the page does not report a delivery that did not happen.
func SendTest(config models.ConfigStruct, override string) (string, error) {
	recipient := strings.TrimSpace(override)
	if recipient == "" {
		recipient = strings.TrimSpace(config.AutotaggerrTestEmail)
	}
	if recipient == "" {
		return "", errors.New("no test recipient is configured")
	}

	subject := "Autotaggerr test message"
	body := "This is a test message from Autotaggerr.\r\n\r\n" +
		"If you are reading it, the SMTP settings on this instance can send mail.\r\n"
	if err := Send(config, []string{recipient}, subject, body); err != nil {
		return "", err
	}
	if redirected, err := redirectInTestEnvironment(config, []string{recipient}); err == nil {
		recipient = redirected[0]
	}
	return recipient, nil
}

// buildMessage renders an RFC 5322 message. CRLF throughout and a Date header are
// not decoration: a bare-LF body is what makes a message land in spam or be rejected
// outright, and a missing Date is the other half of the same complaint.
func buildMessage(from string, to []string, subject, body string, now time.Time) []byte {
	// A header value cannot contain a line break — a subject carrying one would let
	// the caller inject headers of their own into the message.
	subject = sanitizeHeader(subject)
	from = sanitizeHeader(from)

	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", sanitizeHeader(strings.Join(to, ", ")))
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "Date: %s\r\n", now.Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	// Normalize the body's line endings the same way, so a caller writing "\n" does
	// not produce a mixed-ending message.
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n"))
	return []byte(b.String())
}

// sanitizeHeader strips the characters that would end a header line early.
func sanitizeHeader(value string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(strings.TrimSpace(value))
}

// tlsMode resolves the configured encryption mode, folding auto's port rule in so
// the transport below never has to think about the port again. An empty or unknown
// value is auto: this setting arrived after the config did, and an old config.json
// must keep behaving the way it did before it existed.
func tlsMode(config models.ConfigStruct) string {
	switch strings.ToLower(strings.TrimSpace(config.SMTPTLS)) {
	case models.SMTPTLSNone:
		return models.SMTPTLSNone
	case models.SMTPTLSStartTLS:
		return models.SMTPTLSStartTLS
	case models.SMTPTLSImplicit:
		return models.SMTPTLSImplicit
	default:
		if config.SMTPPort == implicitTLSPort {
			return models.SMTPTLSImplicit
		}
		return models.SMTPTLSAuto
	}
}

// sendSMTP opens the connection and hands the message over. Split from Send so the
// message building and the config checks stay testable without a server.
func sendSMTP(config models.ConfigStruct, to []string, message []byte) error {
	address := net.JoinHostPort(config.SMTPHost, strconv.Itoa(config.SMTPPort))

	var auth smtp.Auth
	if config.SMTPUsername != "" {
		auth = smtp.PlainAuth("", config.SMTPUsername, config.SMTPPassword, config.SMTPHost)
	}

	mode := tlsMode(config)
	if mode == models.SMTPTLSImplicit {
		return sendImplicitTLS(address, config.SMTPHost, auth, config.SMTPFrom, to, message)
	}

	conn, err := net.DialTimeout("tcp", address, dialTimeout)
	if err != nil {
		return fmt.Errorf("could not reach the SMTP server: %w", err)
	}
	client, err := smtp.NewClient(conn, config.SMTPHost)
	if err != nil {
		conn.Close()
		return fmt.Errorf("SMTP handshake failed: %w", err)
	}
	defer client.Close()

	offered, _ := client.Extension("STARTTLS")
	switch {
	case mode == models.SMTPTLSNone:
		// Asked for in so many words. smtp.Client still refuses to send PLAIN
		// credentials over the unencrypted link unless the server is localhost, which
		// is the one guardrail worth keeping even here.
	case mode == models.SMTPTLSStartTLS && !offered:
		return errors.New("the SMTP server does not offer STARTTLS, and the TLS mode requires it")
	case offered:
		if err := client.StartTLS(&tls.Config{ServerName: config.SMTPHost}); err != nil {
			return fmt.Errorf("STARTTLS failed: %w", err)
		}
	}
	return deliver(client, auth, config.SMTPFrom, to, message)
}

// sendImplicitTLS is the port-465 path: TLS first, SMTP inside it.
func sendImplicitTLS(address, host string, auth smtp.Auth, from string, to []string, message []byte) error {
	dialer := &net.Dialer{Timeout: dialTimeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", address, &tls.Config{ServerName: host})
	if err != nil {
		return fmt.Errorf("could not reach the SMTP server over TLS: %w", err)
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("SMTP handshake failed: %w", err)
	}
	defer client.Close()
	return deliver(client, auth, from, to, message)
}

// deliver runs the envelope and the data phase on an established client.
func deliver(client *smtp.Client, auth smtp.Auth, from string, to []string, message []byte) error {
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("the SMTP server rejected the credentials: %w", err)
		}
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("the SMTP server rejected the from address: %w", err)
	}
	for _, address := range to {
		if err := client.Rcpt(address); err != nil {
			return fmt.Errorf("the SMTP server rejected %s: %w", address, err)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("the SMTP server refused the message: %w", err)
	}
	if _, err := writer.Write(message); err != nil {
		writer.Close()
		return fmt.Errorf("failed to write the message: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("the SMTP server rejected the message: %w", err)
	}
	return client.Quit()
}
