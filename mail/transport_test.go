package mail

import (
	"bufio"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// fakeSMTP is a scripted SMTP server: enough of the protocol for one message, and
// no more. It exists so the transport — the half that talks to a socket — is
// exercised for real rather than asserted about. The tests bind to 127.0.0.1, which
// is also what lets PLAIN auth run without TLS: smtp.PlainAuth refuses to send
// credentials over an unencrypted link unless the server is localhost.
type fakeSMTP struct {
	advertiseAuth bool
	// advertiseSTARTTLS makes the server offer an upgrade it cannot actually perform
	// — which is what proves whether the client tried: a client that issues STARTTLS
	// against this server fails the handshake, and one that does not, delivers.
	advertiseSTARTTLS bool
	// rejectAt is the command whose response is an error ("MAIL", "RCPT", "DATA",
	// "AUTH"), empty for a server that accepts everything.
	rejectAt string

	mu       sync.Mutex
	received []string // command verbs, in order
	body     string
}

// start runs the server until one client has disconnected, and returns its address.
func (s *fakeSMTP) start(t *testing.T) (host string, port int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { listener.Close() })

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		s.serve(conn)
	}()

	addr := listener.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}

func (s *fakeSMTP) serve(conn net.Conn) {
	reader := bufio.NewReader(conn)
	write := func(line string) { conn.Write([]byte(line + "\r\n")) }

	write("220 fake ESMTP")
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		verb := strings.ToUpper(strings.TrimSpace(line))
		if index := strings.IndexByte(verb, ' '); index > 0 {
			verb = verb[:index]
		}
		s.mu.Lock()
		s.received = append(s.received, verb)
		s.mu.Unlock()

		if verb == s.rejectAt {
			write("550 no")
			continue
		}

		switch verb {
		case "EHLO", "HELO":
			// The first line is the greeting, which net/smtp skips when it collects
			// extensions — an extension announced there is invisible to the client.
			write("250-fake")
			extensions := []string{}
			if s.advertiseAuth {
				extensions = append(extensions, "AUTH PLAIN")
			}
			if s.advertiseSTARTTLS {
				extensions = append(extensions, "STARTTLS")
			}
			extensions = append(extensions, "HELP") // always last, so the reply terminates
			for i, extension := range extensions {
				if i == len(extensions)-1 {
					write("250 " + extension)
				} else {
					write("250-" + extension)
				}
			}
		case "AUTH":
			write("235 authenticated")
		case "MAIL", "RCPT":
			write("250 ok")
		case "DATA":
			write("354 go ahead")
			var body strings.Builder
			for {
				dataLine, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if dataLine == ".\r\n" {
					break
				}
				body.WriteString(dataLine)
			}
			s.mu.Lock()
			s.body = body.String()
			s.mu.Unlock()
			write("250 queued")
		case "QUIT":
			write("221 bye")
			return
		default:
			write("250 ok")
		}
	}
}

func (s *fakeSMTP) commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.received...)
}

func (s *fakeSMTP) delivered() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.body
}

// serverConfig points a config at the fake server. The transport is the real one:
// these tests do not replace `send`.
func serverConfig(host string, port int) models.ConfigStruct {
	return models.ConfigStruct{
		SMTPEnabled: true,
		SMTPHost:    host,
		SMTPPort:    port,
		SMTPFrom:    "autotaggerr@example.com",
	}
}

func TestTransportDeliversWithoutAuth(t *testing.T) {
	server := &fakeSMTP{}
	host, port := server.start(t)

	if err := Send(serverConfig(host, port), []string{"a@example.com"}, "Hello", "Body\n"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got := server.delivered(); !strings.Contains(got, "Subject: Hello") || !strings.Contains(got, "Body") {
		t.Errorf("server received:\n%s", got)
	}
	// No credentials configured means no AUTH command — a relay that wants none
	// must not be sent one.
	for _, command := range server.commands() {
		if command == "AUTH" {
			t.Error("AUTH was issued with no username configured")
		}
	}
}

func TestTransportAuthenticatesWhenConfigured(t *testing.T) {
	server := &fakeSMTP{advertiseAuth: true}
	host, port := server.start(t)

	config := serverConfig(host, port)
	config.SMTPUsername = "user"
	config.SMTPPassword = "secret"

	if err := Send(config, []string{"a@example.com"}, "Hello", "Body"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var authenticated bool
	for _, command := range server.commands() {
		if command == "AUTH" {
			authenticated = true
		}
	}
	if !authenticated {
		t.Errorf("no AUTH command was issued, got %v", server.commands())
	}
}

// TestTransportSurfacesRejections walks the three places a server can say no. Each
// must come back naming what was refused — "the SMTP server rejected the from
// address" is a fixable message; "failed" is not.
func TestTransportSurfacesRejections(t *testing.T) {
	cases := map[string]string{
		"MAIL": "from address",
		"RCPT": "a@example.com",
		"DATA": "refused the message",
	}
	for rejectAt, want := range cases {
		t.Run(rejectAt, func(t *testing.T) {
			server := &fakeSMTP{rejectAt: rejectAt}
			host, port := server.start(t)

			err := Send(serverConfig(host, port), []string{"a@example.com"}, "Hello", "Body")
			if err == nil {
				t.Fatal("expected the rejection to surface")
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to mention %q", err.Error(), want)
			}
		})
	}
}

func TestTransportUnreachableHost(t *testing.T) {
	// Port 1 on the loopback: nothing listens there, and the connection is refused
	// immediately rather than timing out.
	err := Send(serverConfig("127.0.0.1", 1), []string{"a@example.com"}, "Hello", "Body")
	if err == nil || !strings.Contains(err.Error(), "could not reach the SMTP server") {
		t.Errorf("error = %v, want an unreachable-server message", err)
	}
}

// TestTransportImplicitTLSPortUsesTLS proves port 465 takes the implicit-TLS branch:
// nothing is listening there in a test environment, so what is asserted is which
// dial failed — the TLS one, not the plaintext one.
func TestTransportImplicitTLSPortUsesTLS(t *testing.T) {
	config := serverConfig("127.0.0.1", implicitTLSPort)

	err := Send(config, []string{"a@example.com"}, "Hello", "Body")
	if err == nil || !strings.Contains(err.Error(), "over TLS") {
		t.Errorf("error = %v, want the implicit-TLS dial to be the one that failed", err)
	}
}

// TestTransportTLSModes drives the smtp_tls setting against a server that advertises
// STARTTLS but speaks plaintext. That asymmetry is the assertion: a client that
// attempts the upgrade fails the handshake, and one that skips it delivers — so the
// outcome says which branch ran, without needing a certificate.
func TestTransportTLSModes(t *testing.T) {
	cases := map[string]struct {
		mode              string
		advertiseSTARTTLS bool
		wantErr           string // empty means the message must be delivered
	}{
		"auto upgrades when offered": {
			mode: models.SMTPTLSAuto, advertiseSTARTTLS: true, wantErr: "STARTTLS failed",
		},
		"auto sends in clear when not offered": {
			mode: models.SMTPTLSAuto, advertiseSTARTTLS: false,
		},
		"none never upgrades": {
			mode: models.SMTPTLSNone, advertiseSTARTTLS: true,
		},
		"starttls refuses a server that does not offer it": {
			mode: models.SMTPTLSStartTLS, advertiseSTARTTLS: false, wantErr: "does not offer STARTTLS",
		},
		"starttls attempts the upgrade when offered": {
			mode: models.SMTPTLSStartTLS, advertiseSTARTTLS: true, wantErr: "STARTTLS failed",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			server := &fakeSMTP{advertiseSTARTTLS: tc.advertiseSTARTTLS}
			host, port := server.start(t)

			config := serverConfig(host, port)
			config.SMTPTLS = tc.mode

			err := Send(config, []string{"a@example.com"}, "Hello", "Body")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Send: %v", err)
				}
				if got := server.delivered(); !strings.Contains(got, "Subject: Hello") {
					t.Errorf("nothing was delivered, server saw:\n%s", got)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}
