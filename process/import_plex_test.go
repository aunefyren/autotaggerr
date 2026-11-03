package process

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
)

// linkPlexScanMock links the import world's library to a media server, in section
// "7", whose server records every section scan it is asked for and answers it with
// scanStatus. Anything else — the album lookups the tagging pass makes — gets an empty
// container: Plex holds nothing, which is the brand-new album this is about.
func linkPlexScanMock(t *testing.T, w importWorld, scanStatus int) func() []string {
	t.Helper()
	var mu sync.Mutex
	scans := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/library/sections/") && strings.HasSuffix(r.URL.Path, "/refresh") {
			mu.Lock()
			scans = append(scans, r.URL.Path)
			mu.Unlock()
			rw.WriteHeader(scanStatus)
			return
		}
		rw.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(rw, `<MediaContainer size="0"></MediaContainer>`)
	}))
	t.Cleanup(server.Close)

	row := models.MediaServer{Name: "Plex", Type: models.MediaServerTypePlex, Enabled: true, BaseURL: server.URL, Token: "token"}
	if err := w.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	link := models.LibraryMediaServer{LibraryID: w.library.ID, MediaServerID: row.ID, SectionKey: "7"}
	if err := w.db.Create(&link).Error; err != nil {
		t.Fatal(err)
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), scans...)
	}
}

// TestImportAsksMediaServersToScan: an import places files at paths no media server
// has seen, so the album refresh reaches none of them; the library's servers are
// asked to scan its section, which is the only thing that makes a new album appear.
func TestImportAsksMediaServersToScan(t *testing.T) {
	w := newImportWorld(t)
	scanned := linkPlexScanMock(t, w, http.StatusOK)

	w.run(t, modules.TransferMove, w.runner.ProposeImport(w.library, w.release, "", w.sources()))

	if scans := scanned(); len(scans) != 1 || scans[0] != "/library/sections/7/refresh" {
		t.Fatalf("section scans = %v, want one of the linked section 7", scans)
	}
	var event models.Event
	if err := w.db.Where("type = ?", models.EventTypeFileImport).First(&event).Error; err != nil {
		t.Fatal(err)
	}
	if event.Status != models.EventStatusOK {
		t.Errorf("event status = %s (%s)", event.Status, event.Summary)
	}
	if _, warned := event.Details[models.EventDetailWarnings]; warned {
		t.Errorf("a scan that was accepted warns of nothing: %v", event.Details[models.EventDetailWarnings])
	}
}

// TestImportScanRefusedIsAWarning: a server that will not scan does not fail the
// import — the files are in place and tagged, and the server's own scan finds them —
// but the event says so rather than leaving a missing album unexplained.
func TestImportScanRefusedIsAWarning(t *testing.T) {
	w := newImportWorld(t)
	scanned := linkPlexScanMock(t, w, http.StatusUnauthorized)

	w.run(t, modules.TransferMove, w.runner.ProposeImport(w.library, w.release, "", w.sources()))

	if scans := scanned(); len(scans) != 1 {
		t.Fatalf("section scans = %v, want one attempt", scans)
	}
	var event models.Event
	if err := w.db.Where("type = ?", models.EventTypeFileImport).First(&event).Error; err != nil {
		t.Fatal(err)
	}
	if event.Status != models.EventStatusOK {
		t.Errorf("event status = %s (%s); a refused scan is not a failed import", event.Status, event.Summary)
	}
	warnings, _ := event.Details[models.EventDetailWarnings].([]any)
	if len(warnings) != 1 || !strings.Contains(fmt.Sprint(warnings[0]), "could not ask Plex to scan") {
		t.Errorf("warnings = %v, want one naming the refused scan", event.Details[models.EventDetailWarnings])
	}
}
