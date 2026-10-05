package routers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
)

// TestCRUDRejectsMalformedBodies: every create and update binds a JSON object, and a
// body that is not one is the caller's error — a 400 before anything is read or
// written, on both halves of every entity.
func TestCRUDRejectsMalformedBodies(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)

	ds := createEntity(t, r, token, "/api/v1/data-sources", map[string]any{"name": "MB", "type": models.DataSourceTypeMusicBrainz})
	mgr := createEntity(t, r, token, "/api/v1/managers", map[string]any{"name": "M", "type": models.ManagerTypeAutotaggerr})
	tp := createEntity(t, r, token, "/api/v1/tagger-profiles", map[string]any{"name": "P"})
	lib := createEntity(t, r, token, "/api/v1/libraries", map[string]any{"name": "L", "path": "/music"})
	ap := createEntity(t, r, token, "/api/v1/auth-providers", map[string]any{
		"name": "IdP", "issuer": "https://id.example.com", "client_id": "c", "client_secret": "s",
	})

	for _, tc := range []struct {
		collection string
		id         any
	}{
		{"/api/v1/data-sources", ds["id"]},
		{"/api/v1/managers", mgr["id"]},
		{"/api/v1/tagger-profiles", tp["id"]},
		{"/api/v1/libraries", lib["id"]},
		{"/api/v1/auth-providers", ap["id"]},
	} {
		t.Run(tc.collection, func(t *testing.T) {
			w := do(r, "POST", tc.collection, token, "not-an-object")
			expectStatus(t, "POST", w.Code, w.Body.String(), http.StatusBadRequest, "invalid body")
			w = do(r, "PUT", tc.collection+"/"+tc.id.(string), token, "not-an-object")
			expectStatus(t, "PUT", w.Code, w.Body.String(), http.StatusBadRequest, "invalid body")
		})
	}
}

// TestCRUDReportsWriteFailures: a create, update or delete the database refuses is a
// 500 with a message saying which, never a 2xx that claims the row was stored.
func TestCRUDReportsWriteFailures(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	ds := createEntity(t, r, token, "/api/v1/data-sources", map[string]any{"name": "MB", "type": models.DataSourceTypeMusicBrainz})
	mgr := createEntity(t, r, token, "/api/v1/managers", map[string]any{"name": "M", "type": models.ManagerTypeAutotaggerr})
	tp := createEntity(t, r, token, "/api/v1/tagger-profiles", map[string]any{"name": "P"})
	lib := createEntity(t, r, token, "/api/v1/libraries", map[string]any{"name": "L", "path": "/music"})
	ap := createEntity(t, r, token, "/api/v1/auth-providers", map[string]any{
		"name": "IdP", "issuer": "https://id.example.com", "client_id": "c", "client_secret": "s",
	})

	cases := []struct {
		collection string
		model      any
		id         string
		create     map[string]any
		update     map[string]any
	}{
		{"/api/v1/data-sources", &models.DataSource{}, ds["id"].(string),
			map[string]any{"name": "MB2", "type": models.DataSourceTypeMusicBrainz}, map[string]any{"name": "renamed"}},
		{"/api/v1/managers", &models.Manager{}, mgr["id"].(string),
			map[string]any{"name": "M2", "type": models.ManagerTypeAutotaggerr}, map[string]any{"name": "renamed"}},
		{"/api/v1/tagger-profiles", &models.TaggerProfile{}, tp["id"].(string),
			map[string]any{"name": "P2"}, map[string]any{"name": "renamed"}},
		{"/api/v1/libraries", &models.Library{}, lib["id"].(string),
			map[string]any{"name": "L2", "path": "/other"}, map[string]any{"name": "renamed"}},
		{"/api/v1/auth-providers", &models.AuthProvider{}, ap["id"].(string),
			map[string]any{"name": "IdP2", "issuer": "https://id2.example.com", "client_id": "c", "client_secret": "s"},
			map[string]any{"name": "renamed"}},
	}
	for _, tc := range cases {
		t.Run(tc.collection, func(t *testing.T) {
			failWrites(t, api.DB, tc.model, "INSERT")
			failWrites(t, api.DB, tc.model, "UPDATE")
			w := do(r, "POST", tc.collection, token, tc.create)
			expectStatus(t, "POST", w.Code, w.Body.String(), http.StatusInternalServerError, "create failed")
			w = do(r, "PUT", tc.collection+"/"+tc.id, token, tc.update)
			expectStatus(t, "PUT", w.Code, w.Body.String(), http.StatusInternalServerError, "update failed")
		})
	}

	// Delete goes through the shared deleteEntity, so one entity proves it.
	failWrites(t, api.DB, &models.TaggerProfile{}, "DELETE")
	w := do(r, "DELETE", "/api/v1/tagger-profiles/"+tp["id"].(string), token, nil)
	expectStatus(t, "DELETE", w.Code, w.Body.String(), http.StatusInternalServerError, "delete failed")
}

// TestListsReportReadFailures: each list endpoint answers 500 when its table cannot
// be read, rather than an empty list that would read as "nothing configured".
func TestListsReportReadFailures(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	for _, tc := range []struct {
		path  string
		model any
		want  string
	}{
		{"/api/v1/libraries", &models.Library{}, "failed to list libraries"},
		{"/api/v1/managers", &models.Manager{}, "failed to list managers"},
		{"/api/v1/data-sources", &models.DataSource{}, "failed to list data sources"},
		{"/api/v1/tagger-profiles", &models.TaggerProfile{}, "failed to list tagger profiles"},
		{"/api/v1/auth-providers", &models.AuthProvider{}, "failed to list auth providers"},
	} {
		dropTable(t, api.DB, tc.model)
		w := do(r, "GET", tc.path, token, nil)
		expectStatus(t, "GET "+tc.path, w.Code, w.Body.String(), http.StatusInternalServerError, tc.want)
	}

	// The public login-provider list reads the same table.
	w := do(r, "GET", "/api/v1/auth/providers", "", nil)
	expectStatus(t, "GET /auth/providers", w.Code, w.Body.String(), http.StatusInternalServerError, "failed to list login providers")
}

// TestSingletonDataSourceCountFailure: the singleton guard has to count existing rows
// before it creates one, and a failed count must refuse rather than risk a duplicate.
func TestSingletonDataSourceCountFailure(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	dropTable(t, api.DB, &models.DataSource{})

	w := do(r, "POST", "/api/v1/data-sources", token, map[string]any{"name": "A", "type": models.DataSourceTypeAcoustID})
	expectStatus(t, "POST acoustid", w.Code, w.Body.String(), http.StatusInternalServerError, "create failed")
}

// TestCRUDAppliesEveryField: each input struct's apply copies every field it carries.
// A field that was declared but never copied would be accepted and silently dropped,
// so the round trip is asserted against what was sent.
func TestCRUDAppliesEveryField(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	ds := createEntity(t, r, token, "/api/v1/data-sources", map[string]any{
		"name": "MB", "type": models.DataSourceTypeMusicBrainz, "contact": "me@example.com",
	})
	if ds["contact"] != "me@example.com" {
		t.Errorf("data source contact = %v, want it stored", ds["contact"])
	}

	dsID := ds["id"].(string)
	mgr := createEntity(t, r, token, "/api/v1/managers", map[string]any{
		"name": "Lidarr", "type": models.ManagerTypeLidarr,
		"lidarr_skip_artist_refresh": true, "default_data_source_id": dsID,
	})
	var storedMgr models.Manager
	if err := api.DB.First(&storedMgr, "id = ?", mgr["id"]).Error; err != nil {
		t.Fatalf("load manager: %v", err)
	}
	if !storedMgr.LidarrSkipArtistRefresh || storedMgr.DefaultDataSourceID == nil || storedMgr.DefaultDataSourceID.String() != dsID {
		t.Errorf("manager = %+v, want skip-refresh and the default data source stored", storedMgr)
	}

	tp := createEntity(t, r, token, "/api/v1/tagger-profiles", map[string]any{
		"name": "Full", "write_tags": false, "remove_values": true, "use_current_artist_name": true,
		"use_custom_artist_delimiter": true, "custom_artist_delimiter": " & ",
		"custom_artist_delimiter_commas": true, "ignore_redundant_contributing_artists": true,
		"max_genres": 3, "mp3_multi_value_tags": true,
	})
	var storedTP models.TaggerProfile
	if err := api.DB.First(&storedTP, "id = ?", tp["id"]).Error; err != nil {
		t.Fatalf("load profile: %v", err)
	}
	if storedTP.WriteTags || !storedTP.RemoveValues || !storedTP.UseCurrentArtistName ||
		!storedTP.UseCustomArtistDelimiter || storedTP.CustomArtistDelimiter != " & " ||
		!storedTP.CustomArtistDelimiterCommas || !storedTP.IgnoreRedundantContributingArtists ||
		storedTP.MaxGenres != 3 || !storedTP.MP3MultiValueTags {
		t.Errorf("tagger profile = %+v, want every field sent", storedTP)
	}

	lib := createEntity(t, r, token, "/api/v1/libraries", map[string]any{
		"name": "L", "path": "/music", "manager_id": mgr["id"], "tagger_profile_id": tp["id"],
	})
	var storedLib models.Library
	if err := api.DB.First(&storedLib, "id = ?", lib["id"]).Error; err != nil {
		t.Fatalf("load library: %v", err)
	}
	if storedLib.ManagerID == nil || storedLib.ManagerID.String() != mgr["id"] ||
		storedLib.TaggerProfileID == nil || storedLib.TaggerProfileID.String() != tp["id"] {
		t.Errorf("library = %+v, want the manager and profile references stored", storedLib)
	}

	ap := createEntity(t, r, token, "/api/v1/auth-providers", map[string]any{
		"name": "IdP", "issuer": "https://id.example.com/", "client_id": "c", "client_secret": "s",
		"enabled": false, "scopes": "openid email", "redirect_url": "https://app.example.com/cb",
		"default_role": models.UserRoleAdmin,
	})
	var storedAP models.AuthProvider
	if err := api.DB.First(&storedAP, "id = ?", ap["id"]).Error; err != nil {
		t.Fatalf("load auth provider: %v", err)
	}
	if storedAP.Enabled || storedAP.Scopes != "openid email" || storedAP.RedirectURL != "https://app.example.com/cb" ||
		storedAP.Issuer != "https://id.example.com" {
		t.Errorf("auth provider = %+v, want every field sent (issuer trimmed)", storedAP)
	}
}

// TestUpdateManagerAndAuthProviderValidation: the update paths re-check what the
// create paths check, so an edit cannot turn a valid row into an unusable one.
func TestUpdateManagerAndAuthProviderValidation(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)

	mgr := createEntity(t, r, token, "/api/v1/managers", map[string]any{"name": "M", "type": models.ManagerTypeAutotaggerr})
	w := do(r, "PUT", "/api/v1/managers/"+mgr["id"].(string), token, map[string]any{"type": "beets"})
	expectStatus(t, "PUT manager type", w.Code, w.Body.String(), http.StatusBadRequest, "invalid manager type")

	ap := createEntity(t, r, token, "/api/v1/auth-providers", map[string]any{
		"name": "IdP", "issuer": "https://id.example.com", "client_id": "c", "client_secret": "s",
	})
	w = do(r, "PUT", "/api/v1/auth-providers/"+ap["id"].(string), token, map[string]any{"issuer": "http://id.example.com"})
	expectStatus(t, "PUT auth provider issuer", w.Code, w.Body.String(), http.StatusBadRequest, "https")
}

// TestTestManagerFailures: the connection test distinguishes a malformed id, a
// missing manager and a row whose type no constructor knows.
func TestTestManagerFailures(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	w := do(r, "POST", "/api/v1/managers/not-a-uuid/test", token, nil)
	expectStatus(t, "bad id", w.Code, w.Body.String(), http.StatusBadRequest, "invalid id")

	w = do(r, "POST", "/api/v1/managers/"+uuid.New().String()+"/test", token, nil)
	expectStatus(t, "absent", w.Code, w.Body.String(), http.StatusNotFound, "not found")

	// Written straight to the table: the API refuses this type, but a row from an older
	// version (or a hand edit) can still hold it.
	bogus := models.Manager{Name: "Old", Type: "beets", Enabled: true}
	if err := api.DB.Create(&bogus).Error; err != nil {
		t.Fatalf("create manager: %v", err)
	}
	w = do(r, "POST", "/api/v1/managers/"+bogus.ID.String()+"/test", token, nil)
	expectStatus(t, "unsupported type", w.Code, w.Body.String(), http.StatusBadRequest, "unsupported manager type")
}

// TestDeleteManagerSurvivesDetachFailure: handing the manager's artists back is
// best-effort. A manager the user asked to delete must still be deleted when that
// step cannot run.
func TestDeleteManagerSurvivesDetachFailure(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	mgr := createEntity(t, r, token, "/api/v1/managers", map[string]any{"name": "M", "type": models.ManagerTypeAutotaggerr})
	dropTable(t, api.DB, &models.Library{})

	w := do(r, "DELETE", "/api/v1/managers/"+mgr["id"].(string), token, nil)
	expectStatus(t, "DELETE manager", w.Code, w.Body.String(), http.StatusNoContent, "")
	var n int64
	api.DB.Model(&models.Manager{}).Count(&n)
	if n != 0 {
		t.Errorf("managers left = %d, want the row deleted", n)
	}
}

// TestLoginFailures: a body that is not JSON is a 400, an unknown user is the same
// 401 as a wrong password (so usernames cannot be probed), and a server that cannot
// sign a token says so rather than answering with an empty one.
func TestLoginFailures(t *testing.T) {
	r, api := setupAPI(t)

	w := do(r, "POST", "/api/v1/auth/login", "", "not-an-object")
	expectStatus(t, "malformed", w.Code, w.Body.String(), http.StatusBadRequest, "invalid request body")

	w = do(r, "POST", "/api/v1/auth/login", "", map[string]string{"username": "nobody", "password": "pw"})
	expectStatus(t, "unknown user", w.Code, w.Body.String(), http.StatusUnauthorized, "invalid credentials")

	api.SigningKey = nil
	w = do(r, "POST", "/api/v1/auth/login", "", map[string]string{"username": "admin", "password": "pw"})
	expectStatus(t, "no signing key", w.Code, w.Body.String(), http.StatusInternalServerError, "failed to issue token")
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if _, leaked := body["token"]; leaked {
		t.Errorf("a failed issue still returned a token field: %v", body)
	}
}
