package routers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The servers the HTTP endpoint hands an admin, by credential scope.
var (
	mcpReader = mcpServerKey{admin: true, write: false}
	mcpWriter = mcpServerKey{admin: true, write: true}
)

// mcpSession connects an in-memory client to one of the API's MCP servers — the same
// server the HTTP endpoint hands that caller, without the transport in the way.
func mcpSession(t *testing.T, api *API, key mcpServerKey) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	serverSession, err := api.newMCPServer(key).Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// callMCP calls a tool that must succeed and decodes its structured result.
func callMCP[T any](t *testing.T, session *mcp.ClientSession, name string, args map[string]any) T {
	t.Helper()
	res := callMCPRaw(t, session, name, args)
	if res.IsError {
		t.Fatalf("%s returned a tool error: %s", name, mcpText(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("%s: marshal structured content: %v", name, err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s: decode %s: %v", name, raw, err)
	}
	return out
}

func callMCPRaw(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", name, err)
	}
	return res
}

// expectMCPError asserts a tool failed as a readable tool result naming what to do next.
func expectMCPError(t *testing.T, session *mcp.ClientSession, name string, args map[string]any, contains string) {
	t.Helper()
	res := callMCPRaw(t, session, name, args)
	if !res.IsError {
		t.Fatalf("%s: want a tool error mentioning %q, got success", name, contains)
	}
	if text := mcpText(res); !strings.Contains(text, contains) {
		t.Errorf("%s error = %q, want it to mention %q", name, text, contains)
	}
}

func mcpText(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, " ")
}

var mcpPhaseOneTools = []string{
	"get_overview", "get_configuration",
	"list_artists", "get_artist", "list_missing", "get_album", "get_release_tracks",
	"list_files", "get_file",
	"list_activity", "get_activity", "list_migrations",
}

var mcpWriteTools = []string{
	"search_artists", "search_releases", "browse_artist_albums",
	"process", "scan", "refresh_metadata", "tag_files", "cancel_job",
	"add_artist", "follow_artist", "want_album", "unwant_album",
	"attach_file", "release_pin", "confirm_pin", "approve_migration", "dismiss_migration",
}

// listMCPTools returns the tools a server shows, by name.
func listMCPTools(t *testing.T, session *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	out := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		out[tool.Name] = tool
	}
	return out
}

// TestMCPScopeSplit is the line scoped keys exist to hold: a read-only credential is
// shown exactly the read tools, every one marked read-only, so no tool that changes
// anything is reachable with a key that may not write. A writer sees the same reads
// plus the actions, and each action states whether it is destructive.
func TestMCPScopeSplit(t *testing.T) {
	_, api := setupAPI(t)
	for _, admin := range []bool{false, true} {
		reader := listMCPTools(t, mcpSession(t, api, mcpServerKey{admin: admin}))
		if len(reader) != len(mcpPhaseOneTools) {
			t.Errorf("admin=%v reader: %d tools, want %d", admin, len(reader), len(mcpPhaseOneTools))
		}
		for _, name := range mcpPhaseOneTools {
			tool := reader[name]
			if tool == nil {
				t.Errorf("admin=%v reader: %s not listed", admin, name)
				continue
			}
			if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
				t.Errorf("admin=%v reader: %s is not marked read-only", admin, name)
			}
		}

		writer := listMCPTools(t, mcpSession(t, api, mcpServerKey{admin: admin, write: true}))
		if len(writer) != len(mcpPhaseOneTools)+len(mcpWriteTools) {
			t.Errorf("admin=%v writer: %d tools, want %d", admin, len(writer), len(mcpPhaseOneTools)+len(mcpWriteTools))
		}
		for _, name := range mcpWriteTools {
			tool := writer[name]
			if tool == nil {
				t.Errorf("admin=%v writer: %s not listed", admin, name)
				continue
			}
			if tool.Description == "" || tool.Annotations == nil {
				t.Errorf("admin=%v writer: %s has no description or annotations", admin, name)
				continue
			}
			if !tool.Annotations.ReadOnlyHint && tool.Annotations.DestructiveHint == nil {
				t.Errorf("admin=%v writer: action %s does not say whether it is destructive", admin, name)
			}
		}
	}
}

// TestMCPOpenWorldMarksMusicBrainzReads: the tools that may spend the shared
// MusicBrainz budget say so, and the database-only ones do not claim to.
func TestMCPOpenWorldMarksMusicBrainzReads(t *testing.T) {
	_, api := setupAPI(t)
	res, err := mcpSession(t, api, mcpReader).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	openWorld := map[string]bool{"get_album": true, "get_release_tracks": true, "get_file": true}
	for _, tool := range res.Tools {
		got := tool.Annotations.OpenWorldHint != nil && *tool.Annotations.OpenWorldHint
		if got != openWorld[tool.Name] {
			t.Errorf("tool %s openWorldHint = %v, want %v", tool.Name, got, openWorld[tool.Name])
		}
	}
}

// mcpCollectionFixture is one Lidarr-managed artist with an album of each shape the
// tools tell apart.
func mcpCollectionFixture(t *testing.T, api *API) models.CollectionArtist {
	t.Helper()
	artist := artistFixture(t, api.DB, "artist-1", "Kate Bush", models.ManagedByLidarr, true)
	releaseGroupFixture(t, api.DB, artist.MBID, rgFixture{
		mbid: "rg-complete", title: "Hounds of Love", primary: "Album", date: "1985-09-16",
		owned: true, ownedTracks: 12, totalTracks: 12,
		inCatalog: true, catalogOwned: 12, catalogTotal: 12, catalogMonitored: true,
	})
	releaseGroupFixture(t, api.DB, artist.MBID, rgFixture{
		mbid: "rg-partial", title: "The Dreaming", primary: "Album", date: "1982-09-13",
		owned: true, ownedTracks: 5, totalTracks: 10,
		inCatalog: true, catalogOwned: 5, catalogTotal: 10, catalogMonitored: true,
	})
	releaseGroupFixture(t, api.DB, artist.MBID, rgFixture{
		mbid: "rg-missing", title: "Aerial", primary: "Album", date: "2005-11-07",
		inCatalog: true, catalogTotal: 16, catalogMonitored: true,
	})
	// Known but not wanted: the manager does not monitor it.
	releaseGroupFixture(t, api.DB, artist.MBID, rgFixture{
		mbid: "rg-unwanted", title: "Director's Cut", primary: "Album", date: "2011-05-16",
		inCatalog: true, catalogTotal: 11,
	})
	artistFixture(t, api.DB, "artist-2", "Talk Talk", models.ManagedByAutotaggerr, false)
	return artist
}

func TestMCPCollectionTools(t *testing.T) {
	_, api := setupAPI(t)
	mcpCollectionFixture(t, api)
	session := mcpSession(t, api, mcpReader)

	t.Run("list_artists", func(t *testing.T) {
		all := callMCP[mcpArtistList](t, session, "list_artists", nil)
		if all.Total != 2 || len(all.Artists) != 2 || all.NextOffset != nil {
			t.Fatalf("list_artists = %+v, want both artists on one page", all)
		}
		kate := all.Artists[0]
		if kate.Name != "Kate Bush" || kate.Owned != 2 || kate.Complete != 1 || kate.Partial != 1 || kate.Missing != 2 {
			t.Errorf("Kate Bush row = %+v", kate)
		}

		partial := callMCP[mcpArtistList](t, session, "list_artists", map[string]any{"filter": "partial"})
		if partial.Total != 1 || partial.Artists[0].MBID != "artist-1" {
			t.Errorf("filter=partial = %+v, want only Kate Bush", partial)
		}
		byName := callMCP[mcpArtistList](t, session, "list_artists", map[string]any{"name_contains": "TALK"})
		if byName.Total != 1 || byName.Artists[0].MBID != "artist-2" {
			t.Errorf("name_contains=TALK = %+v, want only Talk Talk", byName)
		}
		paged := callMCP[mcpArtistList](t, session, "list_artists", map[string]any{"limit": 1})
		if len(paged.Artists) != 1 || paged.NextOffset == nil || *paged.NextOffset != 1 {
			t.Errorf("limit=1 = %+v, want one row and next_offset 1", paged)
		}
		expectMCPError(t, session, "list_artists", map[string]any{"filter": "missing"}, "list_missing")
	})

	t.Run("get_artist", func(t *testing.T) {
		detail := callMCP[mcpArtistDetail](t, session, "get_artist", map[string]any{"mbid": "artist-1"})
		if detail.Name != "Kate Bush" || len(detail.Albums) != 4 {
			t.Fatalf("get_artist = %+v, want Kate Bush with 4 albums", detail)
		}
		byMBID := map[string]mcpAlbumRow{}
		for _, album := range detail.Albums {
			byMBID[album.MBID] = album
		}
		if a := byMBID["rg-missing"]; a.Owned || !a.Wanted || a.WantedSource != "manager" {
			t.Errorf("Aerial = %+v, want wanted by the manager and not owned", a)
		}
		if a := byMBID["rg-unwanted"]; a.Wanted {
			t.Errorf("Director's Cut = %+v, want not wanted", a)
		}
		expectMCPError(t, session, "get_artist", map[string]any{"mbid": "nobody"}, "list_artists")
	})

	t.Run("list_missing", func(t *testing.T) {
		missing := callMCP[mcpMissingList](t, session, "list_missing", nil)
		if missing.Total != 1 || missing.Albums[0].MBID != "rg-missing" || missing.Albums[0].Artist.Name != "Kate Bush" {
			t.Fatalf("list_missing = %+v, want only Aerial", missing)
		}
		withPartial := callMCP[mcpMissingList](t, session, "list_missing", map[string]any{"include_incomplete": true})
		if withPartial.Total != 2 {
			t.Fatalf("include_incomplete = %+v, want Aerial and The Dreaming", withPartial)
		}
		// Ordered by artist, then by release date.
		if withPartial.Albums[0].MBID != "rg-partial" || withPartial.Albums[1].MBID != "rg-missing" {
			t.Errorf("order = %s, %s; want The Dreaming (1982) before Aerial (2005)",
				withPartial.Albums[0].Title, withPartial.Albums[1].Title)
		}
		other := callMCP[mcpMissingList](t, session, "list_missing", map[string]any{"artist_mbid": "artist-2"})
		if other.Total != 0 || other.Albums == nil {
			t.Errorf("artist_mbid=artist-2 = %+v, want an empty (not null) list", other)
		}
	})
}

func TestMCPAlbumAndTracks(t *testing.T) {
	_, api := setupAPI(t)
	mcpCollectionFixture(t, api)
	library := mcpLibraryFixture(t, api)
	item := models.LibraryItem{
		LibraryID: library.ID, Path: "/music/Kate Bush/Aerial (2005)/01.flac", Status: models.LibraryItemStatusOK,
		MBReleaseID: "rel-1", MBReleaseTrackID: "trk-1", MBRecordingID: "rec-1",
	}
	if err := api.DB.Create(&item).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}

	api.Meta = &fakeMeta{
		getRGReleases: func(rgID string) ([]models.MusicBrainzReleaseSearchResult, error) {
			ed := models.MusicBrainzReleaseSearchResult{ID: "rel-1", Title: "Aerial", Date: "2005-11-07", Country: "GB"}
			ed.Media = append(ed.Media, struct {
				Format     string `json:"format"`
				TrackCount int    `json:"track-count"`
			}{Format: "CD", TrackCount: 9}, struct {
				Format     string `json:"format"`
				TrackCount int    `json:"track-count"`
			}{Format: "CD", TrackCount: 7})
			return []models.MusicBrainzReleaseSearchResult{ed}, nil
		},
		getRelease: func(mbID string) (models.MusicBrainzReleaseResponse, error) {
			if mbID != "rel-1" {
				return models.MusicBrainzReleaseResponse{}, errors.New("404")
			}
			var rel models.MusicBrainzReleaseResponse
			if err := json.Unmarshal([]byte(`{"id":"rel-1","title":"Aerial","media":[{"position":1,"tracks":[
				{"id":"trk-1","position":1,"number":"1","title":"King of the Mountain","length":293000,"recording":{"id":"rec-1"}},
				{"id":"trk-2","position":2,"number":"2","title":"π","length":369000,"recording":{"id":"rec-2"}}]}]}`), &rel); err != nil {
				t.Fatalf("release fixture: %v", err)
			}
			return rel, nil
		},
	}
	session := mcpSession(t, api, mcpReader)

	album := callMCP[mcpAlbumDetail](t, session, "get_album", map[string]any{"artist_mbid": "artist-1", "album_mbid": "rg-missing"})
	if album.Album.Title != "Aerial" || len(album.Editions) != 1 {
		t.Fatalf("get_album = %+v", album)
	}
	if ed := album.Editions[0]; ed.Formats != "CD + CD" || ed.Tracks != 16 {
		t.Errorf("edition = %+v, want 2×CD with 16 tracks", ed)
	}
	expectMCPError(t, session, "get_album", map[string]any{"artist_mbid": "nobody", "album_mbid": "rg-missing"}, "list_artists")

	tracks := callMCP[mcpReleaseTracks](t, session, "get_release_tracks", map[string]any{"release_mbid": "rel-1"})
	if len(tracks.Tracks) != 2 {
		t.Fatalf("get_release_tracks = %+v, want 2 tracks", tracks)
	}
	if tracks.Tracks[0].FileID != item.ID.String() || tracks.Tracks[1].FileID != "" {
		t.Errorf("file ids = %q, %q; want the indexed file on track 1 only", tracks.Tracks[0].FileID, tracks.Tracks[1].FileID)
	}
	expectMCPError(t, session, "get_release_tracks", map[string]any{"release_mbid": "gone"}, "could not load release")
}

func mcpLibraryFixture(t *testing.T, api *API) models.Library {
	t.Helper()
	library := models.Library{Name: "Music", Path: "/music", Enabled: true}
	if err := api.DB.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	return library
}

func TestMCPFileTools(t *testing.T) {
	_, api := setupAPI(t)
	library := mcpLibraryFixture(t, api)
	failedAt := time.Now()
	for _, item := range []models.LibraryItem{
		{LibraryID: library.ID, Path: "/music/A/a.flac", Status: models.LibraryItemStatusOK, MBReleaseID: "rel-a", MBReleaseTrackID: "t-a"},
		{LibraryID: library.ID, Path: "/music/B/b.flac", Status: models.LibraryItemStatusError, Error: "503 from MusicBrainz", LastErrorTransient: true, LastErrorAt: &failedAt},
		{LibraryID: library.ID, Path: "/music/C/c.flac", Status: models.LibraryItemStatusError, Error: "no such release"},
		{LibraryID: library.ID, Path: "/music/D/d.flac", Status: models.LibraryItemStatusUnmatched, PinHeld: true},
	} {
		if err := api.DB.Create(&item).Error; err != nil {
			t.Fatalf("create item: %v", err)
		}
	}
	session := mcpSession(t, api, mcpReader)

	all := callMCP[mcpFileList](t, session, "list_files", nil)
	if all.Total != 4 || all.Files[0].Path != "/music/A/a.flac" {
		t.Fatalf("list_files = %+v, want 4 files ordered by path", all)
	}
	needsPerson := callMCP[mcpFileList](t, session, "list_files", map[string]any{"status": "error", "transient": false})
	if needsPerson.Total != 1 || needsPerson.Files[0].Path != "/music/C/c.flac" {
		t.Errorf("status=error transient=false = %+v, want only c.flac", needsPerson)
	}
	retrying := callMCP[mcpFileList](t, session, "list_files", map[string]any{"status": "error", "transient": true})
	if retrying.Total != 1 || !retrying.Files[0].ErrorTransient || retrying.Files[0].LastErrorAt == nil {
		t.Errorf("status=error transient=true = %+v, want only b.flac, dated", retrying)
	}
	held := callMCP[mcpFileList](t, session, "list_files", map[string]any{"pin_held": true})
	if held.Total != 1 || !held.Files[0].PinHeld {
		t.Errorf("pin_held = %+v, want only d.flac", held)
	}
	inLibrary := callMCP[mcpFileList](t, session, "list_files", map[string]any{"library_id": library.ID.String(), "path_contains": "/B/"})
	if inLibrary.Total != 1 {
		t.Errorf("library_id + path_contains = %+v, want b.flac", inLibrary)
	}
	expectMCPError(t, session, "list_files", map[string]any{"status": "broken"}, "ok, unmatched or error")
	expectMCPError(t, session, "list_files", map[string]any{"library_id": "nope"}, "get_configuration")

	// An unidentified file still describes itself; the missing diff is a reason, not a failure.
	file := callMCP[mcpFileDetail](t, session, "get_file", map[string]any{
		"id": held.Files[0].ID, "include_tag_diff": true, "include_tempo": true,
	})
	if file.Library != "Music" || file.TagDiff != nil || file.TagDiffError == "" {
		t.Errorf("get_file = %+v, want the library named and a tag_diff_error", file)
	}
	if file.Tempo == nil || file.Tempo.Analysed {
		t.Errorf("tempo = %+v, want reported and not analysed", file.Tempo)
	}
	expectMCPError(t, session, "get_file", map[string]any{"id": "00000000-0000-0000-0000-000000000000"}, "list_files")
	expectMCPError(t, session, "get_file", map[string]any{"id": "nope"}, "list_files")
}

func TestMCPActivityTools(t *testing.T) {
	_, api := setupAPI(t)
	run := models.Event{Type: models.EventTypeProcess, Status: "ok", Title: "Process all libraries", StartedAt: time.Now().Add(-time.Hour)}
	if err := api.DB.Create(&run).Error; err != nil {
		t.Fatalf("create run: %v", err)
	}
	stage := models.Event{Type: "refresh", Status: "error", Title: "Refresh metadata", StartedAt: time.Now(), ParentID: &run.ID}
	if err := api.DB.Create(&stage).Error; err != nil {
		t.Fatalf("create stage: %v", err)
	}
	for i := 0; i < 3; i++ {
		row := models.EventItem{EventID: run.ID, Path: "/music/x.flac", Status: "error", Error: "boom"}
		if err := api.DB.Create(&row).Error; err != nil {
			t.Fatalf("create item: %v", err)
		}
	}
	session := mcpSession(t, api, mcpReader)

	feed := callMCP[mcpActivityList](t, session, "list_activity", nil)
	if feed.Total != 2 || feed.Events[0].ID != stage.ID.String() || feed.Events[0].ParentTitle != run.Title {
		t.Fatalf("list_activity = %+v, want the stage first, naming its run", feed)
	}
	runs := callMCP[mcpActivityList](t, session, "list_activity", map[string]any{"runs_only": true})
	if runs.Total != 1 || runs.Events[0].ChildCount != 1 {
		t.Errorf("runs_only = %+v, want the run with one stage", runs)
	}
	failed := callMCP[mcpActivityList](t, session, "list_activity", map[string]any{"status": "error", "title_contains": "REFRESH"})
	if failed.Total != 1 {
		t.Errorf("status+title = %+v, want the stage", failed)
	}
	expectMCPError(t, session, "list_activity", map[string]any{"parent_id": "nope"}, "event id")

	detail := callMCP[mcpActivityDetail](t, session, "get_activity", map[string]any{"id": run.ID.String(), "item_limit": 2})
	if len(detail.Items) != 2 || !detail.ItemsTruncated || len(detail.Stages) != 1 {
		t.Errorf("get_activity = %+v, want 2 of 3 items (truncated) and one stage", detail)
	}
	expectMCPError(t, session, "get_activity", map[string]any{"id": stage.ParentID.String()[:8]}, "list_activity")
	expectMCPError(t, session, "get_activity", map[string]any{"id": "00000000-0000-0000-0000-000000000000"}, "list_activity")
}

func TestMCPOverviewConfigurationAndMigrations(t *testing.T) {
	_, api := setupAPI(t)
	mcpCollectionFixture(t, api)
	mcpLibraryFixture(t, api)
	manager := models.Manager{Name: "Lidarr", Type: "lidarr", Enabled: true, LidarrBaseURL: "http://lidarr:8686", LidarrAPIKey: "lidarr-secret"}
	if err := api.DB.Create(&manager).Error; err != nil {
		t.Fatalf("create manager: %v", err)
	}
	session := mcpSession(t, api, mcpReader)

	overview := callMCP[mcpOverview](t, session, "get_overview", nil)
	if overview.Version != "test" || overview.Counts.Artists != 2 || overview.Counts.Libraries != 1 || overview.Jobs == nil {
		t.Errorf("get_overview = %+v", overview)
	}

	res := callMCPRaw(t, session, "get_configuration", nil)
	raw, _ := json.Marshal(res.StructuredContent)
	if strings.Contains(string(raw), "lidarr-secret") || strings.Contains(mcpText(res), "lidarr-secret") {
		t.Fatalf("get_configuration leaked a manager credential: %s", raw)
	}
	config := callMCP[mcpConfiguration](t, session, "get_configuration", nil)
	if len(config.Managers) != 1 || config.Managers[0].BaseURL != "http://lidarr:8686" || len(config.Libraries) != 1 {
		t.Errorf("get_configuration = %+v", config)
	}

	migrations := callMCP[mcpMigrationList](t, session, "list_migrations", map[string]any{"status": "open"})
	if migrations.Total != 0 || migrations.Migrations == nil {
		t.Errorf("list_migrations = %+v, want an empty (not null) queue", migrations)
	}
}

// --- over HTTP: what the endpoint itself guarantees ---

const mcpListToolsBody = `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`

func mcpPost(r *gin.Engine, header, value string, extra map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp", strings.NewReader(mcpListToolsBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if header != "" {
		req.Header.Set(header, value)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestMCPEndpointAuthentication(t *testing.T) {
	r, _ := setupAPI(t)

	w := mcpPost(r, "", "", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no credential = %d, want 401: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
		t.Errorf("WWW-Authenticate = %q, want a Bearer challenge", got)
	}

	for _, tc := range []struct{ name, header, value string }{
		{"X-Api-Key", "X-Api-Key", "key-123"},
		{"API key as Bearer", "Authorization", "Bearer key-123"},
		{"session token", "Authorization", "Bearer " + loginToken(t, r)},
	} {
		w := mcpPost(r, tc.header, tc.value, nil)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "list_artists") {
			t.Errorf("%s = %d, want 200 listing the tools: %s", tc.name, w.Code, w.Body.String())
		}
	}

	if w := mcpPost(r, "Authorization", "Bearer wrong-key", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong Bearer key = %d, want 401", w.Code)
	}
	// Accepting a key as a Bearer token is the MCP route's option, not the API's.
	if w := do(r, "GET", "/api/v1/auth/me", "key-123", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("Bearer API key on a REST route = %d, want 401", w.Code)
	}
}

func TestMCPEndpointRefusesCrossOriginBrowsers(t *testing.T) {
	r, _ := setupAPI(t)
	w := mcpPost(r, "X-Api-Key", "key-123", map[string]string{
		"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site",
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("cross-site browser request = %d, want 403: %s", w.Code, w.Body.String())
	}
}

func TestMCPEndpointToggle(t *testing.T) {
	r, _ := setupAPI(t)
	previous := files.ConfigFile.AutotaggerrMCPDisabled
	t.Cleanup(func() { files.ConfigFile.AutotaggerrMCPDisabled = previous })

	files.ConfigFile.AutotaggerrMCPDisabled = true
	if w := mcpPost(r, "X-Api-Key", "key-123", nil); w.Code != http.StatusNotFound {
		t.Errorf("disabled = %d, want 404", w.Code)
	}
	// Checked per request: switching it back on needs no restart.
	files.ConfigFile.AutotaggerrMCPDisabled = false
	if w := mcpPost(r, "X-Api-Key", "key-123", nil); w.Code != http.StatusOK {
		t.Errorf("re-enabled = %d, want 200", w.Code)
	}
}

// mcpToolNames lists the tools an MCP request over HTTP is shown.
func mcpToolNames(t *testing.T, r *gin.Engine, key string) []string {
	t.Helper()
	w := mcpPost(r, "Authorization", "Bearer "+key, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("tools/list = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode tools/list: %v (%s)", err, w.Body.String())
	}
	names := make([]string, 0, len(resp.Result.Tools))
	for _, tool := range resp.Result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestAPIKeyManagement(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)

	type created struct {
		APIKey string        `json:"api_key"`
		Key    models.APIKey `json:"key"`
	}
	create := func(name, scope string) created {
		t.Helper()
		w := do(r, "POST", "/api/v1/auth/me/api-keys", token, map[string]string{"name": name, "scope": scope})
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s = %d: %s", name, w.Code, w.Body.String())
		}
		var out created
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	reader := create("assistant", models.APIKeyScopeRead)
	writer := create("automation", models.APIKeyScopeWrite)
	if reader.APIKey == "" || !strings.HasPrefix(reader.APIKey, reader.Key.Prefix) {
		t.Fatalf("created = %+v, want the key and its prefix", reader)
	}

	if w := do(r, "POST", "/api/v1/auth/me/api-keys", token, map[string]string{"name": "x", "scope": "admin"}); w.Code != http.StatusBadRequest {
		t.Errorf("unknown scope = %d, want 400", w.Code)
	}
	if w := do(r, "POST", "/api/v1/auth/me/api-keys", token, map[string]string{"scope": "read"}); w.Code != http.StatusBadRequest {
		t.Errorf("no name = %d, want 400", w.Code)
	}

	// Listed by name and prefix — never the key, which is only stored hashed.
	w := do(r, "GET", "/api/v1/auth/me/api-keys", token, nil)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), reader.APIKey) || !strings.Contains(w.Body.String(), "assistant") {
		t.Errorf("list = %d %s; want the keys by name, without the secret", w.Code, w.Body.String())
	}

	// Keys are managed from a session only: a key cannot mint or revoke keys.
	req := httptest.NewRequest("POST", "/api/v1/auth/me/api-keys", strings.NewReader(`{"name":"more","scope":"write"}`))
	req.Header.Set("X-Api-Key", writer.APIKey)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("creating a key with a key = %d, want 403", rec.Code)
	}

	// A read key reads the REST API and may not write to it.
	get := httptest.NewRequest("GET", "/api/v1/health", nil)
	get.Header.Set("X-Api-Key", reader.APIKey)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, get)
	if rec.Code != http.StatusOK {
		t.Errorf("read key GET = %d, want 200", rec.Code)
	}
	post := httptest.NewRequest("POST", "/api/v1/scan", nil)
	post.Header.Set("X-Api-Key", reader.APIKey)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, post)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "read-only") {
		t.Errorf("read key POST = %d %s, want 403", rec.Code, rec.Body.String())
	}

	// On MCP the scope is the tool list.
	readTools, writeTools := mcpToolNames(t, r, reader.APIKey), mcpToolNames(t, r, writer.APIKey)
	if len(readTools) != len(mcpPhaseOneTools) || slices.Contains(readTools, "tag_files") {
		t.Errorf("read key sees %v, want only the read tools", readTools)
	}
	if !slices.Contains(writeTools, "tag_files") {
		t.Errorf("write key sees %v, want the actions too", writeTools)
	}

	// Revoked: stops working at once.
	if w := do(r, "DELETE", "/api/v1/auth/me/api-keys/"+reader.Key.ID.String(), token, nil); w.Code != http.StatusOK {
		t.Fatalf("revoke = %d: %s", w.Code, w.Body.String())
	}
	if w := mcpPost(r, "X-Api-Key", reader.APIKey, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("revoked key = %d, want 401", w.Code)
	}
	if w := do(r, "DELETE", "/api/v1/auth/me/api-keys/"+reader.Key.ID.String(), token, nil); w.Code != http.StatusNotFound {
		t.Errorf("revoking twice = %d, want 404", w.Code)
	}
}
