package routers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

// seedIdentifyFixtures creates a library with an unmatched file. No AcoustID data
// source and no library opt-in: the default state every existing install is in.
func seedIdentifyFixtures(t *testing.T, db *gorm.DB) models.LibraryItem {
	t.Helper()
	lib := models.Library{Name: "L", Path: "/m", Enabled: true}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	item := models.LibraryItem{
		LibraryID: lib.ID, Path: "/m/Artist/Album (2020)/01 Track.flac",
		Status: models.LibraryItemStatusUnmatched,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("item: %v", err)
	}
	return item
}

func enableAcoustID(t *testing.T, db *gorm.DB, apiKey string) {
	t.Helper()
	if err := db.Create(&models.DataSource{
		Name: "AcoustID", Type: models.DataSourceTypeAcoustID, Enabled: true, APIKey: apiKey,
	}).Error; err != nil {
		t.Fatalf("data source: %v", err)
	}
}

// TestIdentifyUnavailableWithoutDataSource: with no AcoustID row the feature is
// simply not set up. That is a normal state, reported as unavailable with a reason
// rather than as an error — the first of the three switches.
func TestIdentifyUnavailableWithoutDataSource(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	seedIdentifyFixtures(t, api.DB)

	w := do(r, "GET", "/api/v1/identify", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Available bool   `json:"available"`
		Reason    string `json:"reason"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.Available {
		t.Error("identification reported available with no data source configured")
	}
	if got.Reason == "" {
		t.Error("unavailable without saying why")
	}
}

// TestIdentifyRefusesWithoutDataSource: the action itself must refuse too, not just
// the availability probe.
func TestIdentifyRefusesWithoutDataSource(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	item := seedIdentifyFixtures(t, api.DB)

	w := do(r, "POST", "/api/v1/library-items/"+item.ID.String()+"/identify", token, nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", w.Code, w.Body.String())
	}
}

// TestIdentifyRespectsLibraryOptIn is the third switch: configured globally, but
// off for this library. A library that has not opted in must behave exactly as it
// did before the feature existed.
func TestIdentifyRespectsLibraryOptIn(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	item := seedIdentifyFixtures(t, api.DB)
	enableAcoustID(t, api.DB, "test-key")

	w := do(r, "POST", "/api/v1/library-items/"+item.ID.String()+"/identify", token, nil)
	// 403 when fpcalc is present, 503 when it is not — either way the request is
	// refused and nothing is written. The opt-in is what this asserts.
	if w.Code != http.StatusForbidden && w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 403 or 503: %s", w.Code, w.Body.String())
	}

	var stored models.LibraryItem
	_ = api.DB.First(&stored, "id = ?", item.ID).Error
	if stored.MBReleaseID != "" || stored.Pinned {
		t.Errorf("a refused identification modified the item: %+v", stored)
	}
}

// TestIdentifyNeverWritesACorrelation: even fully configured, identification is a
// suggestion. A recording appears on many releases, so applying its answer
// automatically would write a plausible wrong album into the file's tags.
func TestIdentifyNeverWritesACorrelation(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	item := seedIdentifyFixtures(t, api.DB)
	enableAcoustID(t, api.DB, "test-key")
	if err := api.DB.Model(&models.Library{}).Where("id = ?", item.LibraryID).
		Update("use_acoustid", true).Error; err != nil {
		t.Fatalf("opt in: %v", err)
	}

	// The fixture file does not exist, so this fails at the read or the fingerprint
	// stage; what matters is that no path through the handler persists anything.
	do(r, "POST", "/api/v1/library-items/"+item.ID.String()+"/identify", token, nil)

	var stored models.LibraryItem
	_ = api.DB.First(&stored, "id = ?", item.ID).Error
	if stored.MBReleaseID != "" || stored.MBRecordingID != "" || stored.Pinned {
		t.Errorf("identification wrote a correlation: %+v", stored)
	}
	if stored.Status != models.LibraryItemStatusUnmatched {
		t.Errorf("status changed to %q", stored.Status)
	}
}

// TestAcoustIDAPIKeyIsNeverReturned: the client key is a credential, hidden the
// same way the Lidarr secrets are.
func TestAcoustIDAPIKeyIsNeverReturned(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	enableAcoustID(t, api.DB, "super-secret-key")

	w := do(r, "GET", "/api/v1/data-sources", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if body := w.Body.String(); strings.Contains(body, "super-secret-key") {
		t.Errorf("the API key was returned in the data-source list: %s", body)
	}
}

func TestIdentifyRequiresAuth(t *testing.T) {
	r, api := setupAPI(t)
	item := seedIdentifyFixtures(t, api.DB)

	if w := do(r, "POST", "/api/v1/library-items/"+item.ID.String()+"/identify", "", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	if w := do(r, "GET", "/api/v1/identify", "", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("availability status = %d, want 401", w.Code)
	}
}
