package mail

import (
	"errors"
	"net"
	"strings"
	"testing"
)

func TestTransportSurfacesRejectedCredentials(t *testing.T) {
	server := &fakeSMTP{advertiseAuth: true, rejectAt: "AUTH"}
	host, port := server.start(t)

	config := serverConfig(host, port)
	config.SMTPUsername = "user"
	config.SMTPPassword = "wrong"

	err := Send(config, []string{"a@example.com"}, "Hello", "Body")
	if err == nil || !strings.Contains(err.Error(), "rejected the credentials") {
		t.Errorf("error = %v, want a credentials rejection", err)
	}
}

// TestTransportSurfacesARejectedMessage: a server that reads the whole message and
// then refuses it (a content filter) is a failure, not a delivery.
func TestTransportSurfacesARejectedMessage(t *testing.T) {
	server := &fakeSMTP{rejectBody: true}
	host, port := server.start(t)

	err := Send(serverConfig(host, port), []string{"a@example.com"}, "Hello", "Body")
	if err == nil || !strings.Contains(err.Error(), "rejected the message") {
		t.Errorf("error = %v, want the end-of-data rejection", err)
	}
}

// TestTransportSurfacesABadGreeting: something listening on the port that is not an
// SMTP server willing to talk fails the handshake, not the send.
func TestTransportSurfacesABadGreeting(t *testing.T) {
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
		conn.Write([]byte("554 not accepting connections\r\n"))
	}()

	port := listener.Addr().(*net.TCPAddr).Port
	err = Send(serverConfig("127.0.0.1", port), []string{"a@example.com"}, "Hello", "Body")
	if err == nil || !strings.Contains(err.Error(), "handshake failed") {
		t.Errorf("error = %v, want a handshake failure", err)
	}
}

// TestSendTestReportsAFailedSend: the test-message button must not report a
// delivery the transport refused.
func TestSendTestReportsAFailedSend(t *testing.T) {
	got := capture(t)
	got.err = errors.New("connection refused")

	config := workingConfig()
	config.AutotaggerrTestEmail = "me@example.com"
	recipient, err := SendTest(config, "")
	if err == nil || recipient != "" {
		t.Errorf("SendTest = %q, %v; want no recipient and the transport error", recipient, err)
	}
}
