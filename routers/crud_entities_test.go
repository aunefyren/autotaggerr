package routers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Round-trip tests for the entity CRUD endpoints. They look repetitive because the
// handlers are, but the properties being pinned are not decoration:
//
//   - a pointer-field input patches only what was sent, so editing one field cannot
//     silently reset the others (this is the whole reason the inputs use pointers);
//   - secrets are write-only and never come back out;
//   - a bad UUID is a 400 and a missing row is a 404, distinctly.

// crudCase describes one entity's endpoints so the shared shape is asserted once
// per entity rather than once per handler.
type crudCase struct {
	name       string
	collection string
	create     map[string]any
	update     map[string]any
	// verify checks the decoded body after the update.
	verify func(t *testing.T, body map[string]any)
}

func TestEntityCRUDRoundTrips(t *testing.T) {
	cases := []crudCase{
		{
			name:       "tagger profile",
			collection: "/api/v1/tagger-profiles",
			create:     map[string]any{"name": "Strict", "write_tags": true, "custom_artist_delimiter": "; "},
			update:     map[string]any{"name": "Strict (edited)"},
			verify: func(t *testing.T, body map[string]any) {
				// The update sent only a name, so the other fields must survive it.
				if body["custom_artist_delimiter"] != "; " {
					t.Errorf("custom_artist_delimiter = %v, want the value the create set", body["custom_artist_delimiter"])
				}
				if body["write_tags"] != true {
					t.Errorf("write_tags = %v, want it preserved through a partial update", body["write_tags"])
				}
			},
		},
		{
			name:       "library",
			collection: "/api/v1/libraries",
			create:     map[string]any{"name": "Music", "path": "/music", "cron": "0 3 * * *", "use_acoustid": true},
			update:     map[string]any{"enabled": false},
			verify: func(t *testing.T, body map[string]any) {
				if body["path"] != "/music" {
					t.Errorf("path = %v, want it preserved", body["path"])
				}
				if body["cron"] != "0 3 * * *" {
					t.Errorf("cron = %v, want it preserved", body["cron"])
				}
				// A per-library opt-in that silently resets would quietly disable
				// fingerprinting for the library.
				if body["use_acoustid"] != true {
					t.Errorf("use_acoustid = %v, want it preserved", body["use_acoustid"])
				}
				if body["enabled"] != false {
					t.Errorf("enabled = %v, want false — the update did not apply", body["enabled"])
				}
			},
		},
		{
			name:       "auth provider",
			collection: "/api/v1/auth-providers",
			create: map[string]any{
				"name": "IdP", "type": models.AuthProviderTypeOIDC,
				"issuer": " https://id.example.com/ ", "client_id": "cid", "client_secret": "shhh",
			},
			update: map[string]any{"allow_signup": true},
			verify: func(t *testing.T, body map[string]any) {
				// Issuers are compared as strings during discovery, so a trailing
				// slash or stray space has to be normalised on the way in.
				if body["issuer"] != "https://id.example.com" {
					t.Errorf("issuer = %v, want it trimmed and de-slashed", body["issuer"])
				}
				if body["client_id"] != "cid" {
					t.Errorf("client_id = %v, want it preserved", body["client_id"])
				}
				if _, leaked := body["client_secret"]; leaked {
					t.Error("client_secret came back out of the API")
				}
			},
		},
		{
			name:       "manager",
			collection: "/api/v1/managers",
			create: map[string]any{
				"name": "Lidarr", "type": models.ManagerTypeLidarr,
				"lidarr_base_url": "http://lidarr:8686", "lidarr_api_key": "secret",
			},
			update: map[string]any{"enabled": false},
			verify: func(t *testing.T, body map[string]any) {
				if body["lidarr_base_url"] != "http://lidarr:8686" {
					t.Errorf("lidarr_base_url = %v, want it preserved", body["lidarr_base_url"])
				}
				if _, leaked := body["lidarr_api_key"]; leaked {
					t.Error("lidarr_api_key came back out of the API")
				}
			},
		},
		{
			name:       "data source",
			collection: "/api/v1/data-sources",
			create: map[string]any{
				"name": "fanart.tv", "type": models.DataSourceTypeFanart,
				"base_url": "https://webservice.fanart.tv/v3", "api_key": "secret", "rate_limit": 2.0,
			},
			update: map[string]any{"enabled": false},
			verify: func(t *testing.T, body map[string]any) {
				if body["rate_limit"] != 2.0 {
					t.Errorf("rate_limit = %v, want it preserved", body["rate_limit"])
				}
				if _, leaked := body["api_key"]; leaked {
					t.Error("api_key came back out of the API")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := setupAPI(t)
			token := loginToken(t, r)

			created := createEntity(t, r, token, tc.collection, tc.create)
			id, _ := created["id"].(string)
			if id == "" {
				t.Fatalf("create returned no id: %v", created)
			}
			one := tc.collection + "/" + id

			// Read it back on its own.
			fetched := decodeJSON[map[string]any](t, r, "GET", one, token, nil)
			if fetched["id"] != id {
				t.Errorf("GET returned id %v, want %v", fetched["id"], id)
			}

			// It appears in the list.
			list := decodeJSON[[]map[string]any](t, r, "GET", tc.collection, token, nil)
			if len(list) == 0 {
				t.Fatalf("list is empty after a create")
			}

			updated := decodeJSON[map[string]any](t, r, "PUT", one, token, tc.update)
			tc.verify(t, updated)

			// Delete, then confirm it is gone rather than trusting the 204.
			if w := do(r, "DELETE", one, token, nil); w.Code != http.StatusNoContent {
				t.Fatalf("DELETE = %d, want 204: %s", w.Code, w.Body.String())
			}
			if w := do(r, "GET", one, token, nil); w.Code != http.StatusNotFound {
				t.Errorf("GET after DELETE = %d, want 404", w.Code)
			}
		})
	}
}

func createEntity(t *testing.T, r *gin.Engine, token, path string, body map[string]any) map[string]any {
	t.Helper()
	w := do(r, "POST", path, token, body)
	if w.Code != http.StatusCreated && w.Code != http.StatusOK {
		t.Fatalf("POST %s = %d, want 201: %s", path, w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	return out
}

// TestEntityIDValidation: a malformed id is the caller's error (400) and a
// well-formed id for a row that does not exist is a miss (404). Collapsing the two
// would make a typo look like a deleted entity.
func TestEntityIDValidation(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)
	absent := uuid.New().String()

	for _, collection := range []string{
		"/api/v1/tagger-profiles", "/api/v1/libraries",
		"/api/v1/auth-providers", "/api/v1/managers", "/api/v1/data-sources",
	} {
		t.Run(collection, func(t *testing.T) {
			for _, method := range []string{"GET", "PUT", "DELETE"} {
				if w := do(r, method, collection+"/not-a-uuid", token, map[string]any{}); w.Code != http.StatusBadRequest {
					t.Errorf("%s %s/not-a-uuid = %d, want 400", method, collection, w.Code)
				}
				if w := do(r, method, collection+"/"+absent, token, map[string]any{}); w.Code != http.StatusNotFound {
					t.Errorf("%s %s/<absent> = %d, want 404", method, collection, w.Code)
				}
			}
		})
	}
}

// TestCreateValidation: the required-field and enum checks, which are the only
// server-side guard against a half-built entity.
func TestCreateValidation(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)

	cases := []struct {
		name, path string
		body       any
	}{
		{"tagger profile without a name", "/api/v1/tagger-profiles", map[string]any{}},
		{"tagger profile with an empty name", "/api/v1/tagger-profiles", map[string]any{"name": ""}},
		{"library without a path", "/api/v1/libraries", map[string]any{"name": "Music"}},
		{"library without a name", "/api/v1/libraries", map[string]any{"path": "/music"}},
		{"data source with an unknown type", "/api/v1/data-sources", map[string]any{"name": "X", "type": "discogs"}},
		{"data source without a type", "/api/v1/data-sources", map[string]any{"name": "X"}},
		{"manager with an unknown type", "/api/v1/managers", map[string]any{"name": "X", "type": "beets"}},
		{"auth provider with an unknown type", "/api/v1/auth-providers", map[string]any{"name": "X", "type": "saml"}},
		{"malformed json", "/api/v1/libraries", "not-an-object"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if w := do(r, "POST", tc.path, token, tc.body); w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400: %s", w.Code, w.Body.String())
			}
		})
	}
}

// TestUpdateRejectsUnknownType: the create path validates the type, and so must the
// update path — otherwise a valid entity can be edited into an unsupported one.
func TestUpdateRejectsUnknownType(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)

	source := createEntity(t, r, token, "/api/v1/data-sources", map[string]any{
		"name": "CAA", "type": models.DataSourceTypeCoverArtArchive,
	})
	id, _ := source["id"].(string)

	if w := do(r, "PUT", "/api/v1/data-sources/"+id, token, map[string]any{"type": "discogs"}); w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
}

// TestSecretsSurviveAnUpdateThatOmitsThem: the UI never receives a secret, so it
// cannot resend one. An update that leaves the field out must keep what is stored,
// or every edit silently wipes the credential.
func TestSecretsSurviveAnUpdateThatOmitsThem(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	manager := createEntity(t, r, token, "/api/v1/managers", map[string]any{
		"name": "Lidarr", "type": models.ManagerTypeLidarr,
		"lidarr_base_url": "http://lidarr:8686", "lidarr_api_key": "keep-me",
	})
	id, _ := manager["id"].(string)

	if w := do(r, "PUT", "/api/v1/managers/"+id, token, map[string]any{"name": "Lidarr (renamed)"}); w.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", w.Code, w.Body.String())
	}

	var stored models.Manager
	if err := api.DB.First(&stored, "id = ?", id).Error; err != nil {
		t.Fatalf("reload manager: %v", err)
	}
	if stored.LidarrAPIKey != "keep-me" {
		t.Errorf("lidarr api key = %q, want it untouched by a rename", stored.LidarrAPIKey)
	}

	source := createEntity(t, r, token, "/api/v1/data-sources", map[string]any{
		"name": "fanart", "type": models.DataSourceTypeFanart, "api_key": "keep-me-too",
	})
	sourceID, _ := source["id"].(string)
	if w := do(r, "PUT", "/api/v1/data-sources/"+sourceID, token, map[string]any{"enabled": false}); w.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", w.Code, w.Body.String())
	}
	var storedSource models.DataSource
	if err := api.DB.First(&storedSource, "id = ?", sourceID).Error; err != nil {
		t.Fatalf("reload data source: %v", err)
	}
	if storedSource.APIKey != "keep-me-too" {
		t.Errorf("api key = %q, want it untouched by a toggle", storedSource.APIKey)
	}
}

func TestCRUDRequiresAuth(t *testing.T) {
	r, _ := setupAPI(t)
	for _, path := range []string{
		"/api/v1/tagger-profiles", "/api/v1/libraries",
		"/api/v1/auth-providers", "/api/v1/managers", "/api/v1/data-sources",
	} {
		if w := do(r, "GET", path, "", nil); w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s = %d, want 401", path, w.Code)
		}
		if w := do(r, "POST", path, "", map[string]any{"name": "x"}); w.Code != http.StatusUnauthorized {
			t.Errorf("POST %s = %d, want 401", path, w.Code)
		}
	}
}
