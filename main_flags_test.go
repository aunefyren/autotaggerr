package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/web"
)

// TestParseFlagsOverridesEveryField: each flag writes exactly its own field. A
// copy-paste slip here (smtpusername landing in SMTPFrom, say) would only show up as
// mail silently failing on the one deployment that sets the flag.
func TestParseFlagsOverridesEveryField(t *testing.T) {
	cfg := models.ConfigStruct{AutotaggerrPort: 8080, SMTPEnabled: true}
	got, _, _ := runParseFlags(t, []string{
		"-externalurl", "https://tags.example",
		"-tz", "Europe/Oslo",
		"-disablesmtp", "TRUE",
		"-smtphost", "smtp.example",
		"-smtpport", "2525",
		"-smtpusername", "user",
		"-smtppassword", "secret",
		"-smtpfrom", "from@example",
	}, cfg)

	want := cfg
	want.AutotaggerrExternalURL = "https://tags.example"
	want.Timezone = "Europe/Oslo"
	want.SMTPEnabled = false
	want.SMTPHost = "smtp.example"
	want.SMTPPort = 2525
	want.SMTPUsername = "user"
	want.SMTPPassword = "secret"
	want.SMTPFrom = "from@example"

	checks := []struct {
		name      string
		got, want any
	}{
		{"externalurl", got.AutotaggerrExternalURL, want.AutotaggerrExternalURL},
		{"tz", got.Timezone, want.Timezone},
		{"disablesmtp", got.SMTPEnabled, want.SMTPEnabled},
		{"smtphost", got.SMTPHost, want.SMTPHost},
		{"smtpport", got.SMTPPort, want.SMTPPort},
		{"smtpusername", got.SMTPUsername, want.SMTPUsername},
		{"smtppassword", got.SMTPPassword, want.SMTPPassword},
		{"smtpfrom", got.SMTPFrom, want.SMTPFrom},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("-%s: got %v, want %v", c.name, c.got, c.want)
		}
	}
}

// TestParseFlagsDisableSMTPFalseKeepsItOn: the flag is a string, and only "true"
// disables — anything else passed explicitly re-enables.
func TestParseFlagsDisableSMTPFalseKeepsItOn(t *testing.T) {
	got, _, _ := runParseFlags(t, []string{"-disablesmtp", "false"}, models.ConfigStruct{AutotaggerrPort: 8080})
	if !got.SMTPEnabled {
		t.Error("-disablesmtp false left SMTP disabled")
	}
}

// TestRouterDirectoryIsNotAnAsset: a path naming a directory in the bundle passes the
// fs.Stat check but has no bytes to serve. It must 404, not fall back to the SPA or
// serve an empty 200.
func TestRouterDirectoryIsNotAnAsset(t *testing.T) {
	r := testRouter(t)
	if w := routerGet(t, r, "/assets"); w.Code != http.StatusNotFound {
		t.Errorf("GET /assets = %d, want 404", w.Code)
	}
}

// TestRouterServesFontsAsBinary: the fonts are served as themselves whatever the
// host's MIME table knows about them — octet-stream when it knows nothing.
func TestRouterServesFontsAsBinary(t *testing.T) {
	r := testRouter(t)

	distFS, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		t.Fatalf("open embedded assets: %v", err)
	}
	entries, _ := fs.ReadDir(distFS, "assets")
	var font string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".woff") {
			font = "assets/" + entry.Name()
			break
		}
	}
	if font == "" {
		t.Skip("the embedded bundle has no .woff to test against")
	}

	w := routerGet(t, r, "/"+font)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /%s = %d, want 200", font, w.Code)
	}
	if ctype := w.Header().Get("Content-Type"); ctype == "" || strings.Contains(ctype, "html") {
		t.Errorf("GET /%s content type = %q, want a font or octet-stream", font, ctype)
	}
}

// TestRouterCORSAcceptsAnyOrigin: the API is consumed from any origin (the UI may be
// reverse-proxied under another host), so a cross-origin request is allowed. The
// "*" entry in AllowOrigins is what decides this; AllowOriginFunc is never consulted.
func TestRouterCORSAcceptsAnyOrigin(t *testing.T) {
	r := testRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	req.Header.Set("Origin", "https://elsewhere.example")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("cross-origin GET /api/ping = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want *", got)
	}
}
