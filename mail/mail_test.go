package mail

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// workingConfig is the smallest configuration a send is allowed to proceed from.
func workingConfig() models.ConfigStruct {
	return models.ConfigStruct{
		SMTPEnabled: true,
		SMTPHost:    "smtp.example.com",
		SMTPPort:    587,
		SMTPFrom:    "autotaggerr@example.com",
	}
}

// capture replaces the transport for one test and returns what a send handed it.
func capture(t *testing.T) *struct {
	called  bool
	to      []string
	message []byte
	err     error
} {
	t.Helper()
	got := &struct {
		called  bool
		to      []string
		message []byte
		err     error
	}{}
	original := send
	send = func(_ models.ConfigStruct, to []string, message []byte) error {
		got.called = true
		got.to = to
		got.message = message
		return got.err
	}
	t.Cleanup(func() { send = original })
	return got
}

func TestConfigured(t *testing.T) {
	cases := map[string]struct {
		mutate func(*models.ConfigStruct)
		want   bool
	}{
		"complete":    {func(*models.ConfigStruct) {}, true},
		"disabled":    {func(c *models.ConfigStruct) { c.SMTPEnabled = false }, false},
		"no host":     {func(c *models.ConfigStruct) { c.SMTPHost = "" }, false},
		"no from":     {func(c *models.ConfigStruct) { c.SMTPFrom = "" }, false},
		"no port":     {func(c *models.ConfigStruct) { c.SMTPPort = 0 }, false},
		"port unset":  {func(c *models.ConfigStruct) { c.SMTPPort = -1 }, false},
		"with auth":   {func(c *models.ConfigStruct) { c.SMTPUsername = "user" }, true},
		"port 465 ok": {func(c *models.ConfigStruct) { c.SMTPPort = 465 }, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			config := workingConfig()
			tc.mutate(&config)
			if got := Configured(config); got != tc.want {
				t.Errorf("Configured = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSendRefusesIncompleteConfig covers the checks that must happen before a
// connection is attempted: a misconfigured instance should say what is missing, not
// time out against a host that is not there.
func TestSendRefusesIncompleteConfig(t *testing.T) {
	cases := map[string]struct {
		mutate func(*models.ConfigStruct)
		to     []string
		want   string
	}{
		"no recipient":    {func(*models.ConfigStruct) {}, nil, "no recipient"},
		"blank recipient": {func(*models.ConfigStruct) {}, []string{"  "}, "no recipient"},
		"disabled":        {func(c *models.ConfigStruct) { c.SMTPEnabled = false }, []string{"a@example.com"}, "disabled"},
		"no host":         {func(c *models.ConfigStruct) { c.SMTPHost = "" }, []string{"a@example.com"}, "host"},
		"no port":         {func(c *models.ConfigStruct) { c.SMTPPort = 0 }, []string{"a@example.com"}, "port"},
		"no from":         {func(c *models.ConfigStruct) { c.SMTPFrom = "" }, []string{"a@example.com"}, "from address"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := capture(t)
			config := workingConfig()
			tc.mutate(&config)

			err := Send(config, tc.to, "Subject", "Body")
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err.Error(), tc.want)
			}
			if got.called {
				t.Error("the transport was called despite an incomplete configuration")
			}
		})
	}
}

func TestSendPassesRecipientsAndMessage(t *testing.T) {
	got := capture(t)

	err := Send(workingConfig(), []string{"one@example.com", " ", "two@example.com"}, "Hello", "Body line\n")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !got.called {
		t.Fatal("the transport was never called")
	}
	if len(got.to) != 2 || got.to[0] != "one@example.com" || got.to[1] != "two@example.com" {
		t.Errorf("recipients = %v, want the two non-blank addresses", got.to)
	}
	if !strings.Contains(string(got.message), "Subject: Hello") {
		t.Errorf("message did not carry the subject:\n%s", got.message)
	}
}

// TestSendReportsTransportFailure is what the Settings page depends on: a server
// that refuses must surface as an error the admin sees, not a silent success.
func TestSendReportsTransportFailure(t *testing.T) {
	got := capture(t)
	got.err = errors.New("connection refused")

	err := Send(workingConfig(), []string{"a@example.com"}, "Hello", "Body")
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("Send error = %v, want the transport's failure", err)
	}
}

func TestSendTestUsesConfiguredRecipient(t *testing.T) {
	got := capture(t)
	config := workingConfig()
	config.AutotaggerrTestEmail = "admin@example.com"

	recipient, err := SendTest(config, "")
	if err != nil {
		t.Fatalf("SendTest: %v", err)
	}
	if recipient != "admin@example.com" {
		t.Errorf("recipient = %q, want the configured test address", recipient)
	}
	if len(got.to) != 1 || got.to[0] != "admin@example.com" {
		t.Errorf("recipients = %v, want the configured test address", got.to)
	}
}

func TestSendTestOverrideWins(t *testing.T) {
	got := capture(t)
	config := workingConfig()
	config.AutotaggerrTestEmail = "admin@example.com"

	recipient, err := SendTest(config, " other@example.com ")
	if err != nil {
		t.Fatalf("SendTest: %v", err)
	}
	if recipient != "other@example.com" {
		t.Errorf("recipient = %q, want the override", recipient)
	}
	if len(got.to) != 1 || got.to[0] != "other@example.com" {
		t.Errorf("recipients = %v, want the override", got.to)
	}
}

func TestSendTestWithoutRecipient(t *testing.T) {
	got := capture(t)

	_, err := SendTest(workingConfig(), "")
	if err == nil || !strings.Contains(err.Error(), "test recipient") {
		t.Errorf("error = %v, want it to name the missing test recipient", err)
	}
	if got.called {
		t.Error("the transport was called with no recipient")
	}
}

// TestBuildMessage pins the wire format: CRLF endings, a Date, and a body that is
// separated from the headers by a blank line. A message that gets any of these
// wrong is accepted by a permissive server and filed as spam by everyone else.
func TestBuildMessage(t *testing.T) {
	when := time.Date(2026, 8, 7, 12, 30, 0, 0, time.UTC)
	message := string(buildMessage("from@example.com", []string{"a@example.com", "b@example.com"},
		"Subject line", "First\nSecond\n", when))

	for _, want := range []string{
		"From: from@example.com\r\n",
		"To: a@example.com, b@example.com\r\n",
		"Subject: Subject line\r\n",
		"MIME-Version: 1.0\r\n",
		"Content-Type: text/plain; charset=utf-8\r\n",
		"\r\n\r\nFirst\r\nSecond\r\n",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("message is missing %q:\n%s", want, message)
		}
	}
	if !strings.Contains(message, "Date: "+when.Format(time.RFC1123Z)) {
		t.Errorf("message carries no Date header:\n%s", message)
	}
	if strings.Contains(strings.ReplaceAll(message, "\r\n", ""), "\n") {
		t.Errorf("message contains a bare LF:\n%q", message)
	}
}

// TestBuildMessageRejectsHeaderInjection: a subject carrying a line break would
// otherwise let its caller append headers of its own.
func TestBuildMessageRejectsHeaderInjection(t *testing.T) {
	message := string(buildMessage("from@example.com", []string{"a@example.com"},
		"Hello\r\nBcc: victim@example.com", "Body", time.Now()))

	if strings.Contains(message, "Bcc:") && strings.Contains(message, "\r\nBcc:") {
		t.Errorf("a header break survived into the message:\n%s", message)
	}
	headers, _, ok := strings.Cut(message, "\r\n\r\n")
	if !ok {
		t.Fatalf("message has no header/body split:\n%s", message)
	}
	// Six headers, so five separators — the sixth CRLF is the one the split ate.
	if strings.Count(headers, "\r\n") != 5 {
		t.Errorf("expected exactly six header lines, got:\n%s", headers)
	}
}

// TestTestEnvironmentRedirectsEveryRecipient is the rule with no exception: a test
// instance is typically pointed at a copy of the real database, so a message that
// honoured its recipients would mail real people from a scratch deployment.
func TestTestEnvironmentRedirectsEveryRecipient(t *testing.T) {
	got := capture(t)
	config := workingConfig()
	config.AutotaggerrEnvironment = "test"
	config.AutotaggerrTestEmail = "sink@example.com"

	err := Send(config, []string{"real@example.com", "another@example.com"}, "Hello", "Body")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(got.to) != 1 || got.to[0] != "sink@example.com" {
		t.Fatalf("envelope recipients = %v, want only the test address", got.to)
	}
	// The header has to agree with the envelope, or the message names people it was
	// never delivered to.
	message := string(got.message)
	if strings.Contains(message, "real@example.com") || strings.Contains(message, "another@example.com") {
		t.Errorf("a real address survived into the message:\n%s", message)
	}
	if !strings.Contains(message, "To: sink@example.com") {
		t.Errorf("message is not addressed to the test recipient:\n%s", message)
	}
}

// TestTestEnvironmentIgnoresTheOverride: SendTest's explicit address is exactly the
// kind of exception that gets used by accident, so it is not one.
func TestTestEnvironmentIgnoresTheOverride(t *testing.T) {
	got := capture(t)
	config := workingConfig()
	config.AutotaggerrEnvironment = "TEST" // case must not matter
	config.AutotaggerrTestEmail = "sink@example.com"

	recipient, err := SendTest(config, "someone@example.com")
	if err != nil {
		t.Fatalf("SendTest: %v", err)
	}
	if len(got.to) != 1 || got.to[0] != "sink@example.com" {
		t.Errorf("recipients = %v, want the override to be ignored", got.to)
	}
	// And the caller is told where it actually went, not where it asked.
	if recipient != "sink@example.com" {
		t.Errorf("reported recipient = %q, want the address it was delivered to", recipient)
	}
}

// TestTestEnvironmentWithoutASinkRefuses: with nowhere safe to send, the send is
// refused rather than falling back to the real recipient.
func TestTestEnvironmentWithoutASinkRefuses(t *testing.T) {
	got := capture(t)
	config := workingConfig()
	config.AutotaggerrEnvironment = "test"

	err := Send(config, []string{"real@example.com"}, "Hello", "Body")
	if err == nil || !strings.Contains(err.Error(), "test environment") {
		t.Errorf("error = %v, want a refusal naming the test environment", err)
	}
	if got.called {
		t.Error("a message was sent from a test instance with no test recipient")
	}
}

// TestProductionDoesNotRedirect is the control: the redirect must not leak into a
// real deployment that happens to have a test address configured.
func TestProductionDoesNotRedirect(t *testing.T) {
	got := capture(t)
	config := workingConfig()
	config.AutotaggerrEnvironment = "prod"
	config.AutotaggerrTestEmail = "sink@example.com"

	if err := Send(config, []string{"real@example.com"}, "Hello", "Body"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(got.to) != 1 || got.to[0] != "real@example.com" {
		t.Errorf("recipients = %v, want the real address in production", got.to)
	}
}

// TestTLSModeResolution pins how the setting and the port combine — the port only
// speaks when the mode does not.
func TestTLSModeResolution(t *testing.T) {
	cases := []struct {
		mode string
		port int
		want string
	}{
		{"", 587, models.SMTPTLSAuto},     // an old config.json keeps its behaviour
		{"", 465, models.SMTPTLSImplicit}, // ... including the port rule
		{models.SMTPTLSAuto, 465, models.SMTPTLSImplicit},
		{models.SMTPTLSAuto, 25, models.SMTPTLSAuto},
		{models.SMTPTLSNone, 465, models.SMTPTLSNone}, // an explicit mode beats the port
		{models.SMTPTLSStartTLS, 465, models.SMTPTLSStartTLS},
		{models.SMTPTLSImplicit, 587, models.SMTPTLSImplicit},
		{"  StartTLS  ", 587, models.SMTPTLSStartTLS}, // tolerant of what a hand-edited file holds
		{"nonsense", 587, models.SMTPTLSAuto},
	}
	for _, tc := range cases {
		config := workingConfig()
		config.SMTPTLS = tc.mode
		config.SMTPPort = tc.port
		if got := tlsMode(config); got != tc.want {
			t.Errorf("tlsMode(%q, port %d) = %q, want %q", tc.mode, tc.port, got, tc.want)
		}
	}
}
