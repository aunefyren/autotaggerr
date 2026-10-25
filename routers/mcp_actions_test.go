package routers

import (
	"errors"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/metadata"
	"github.com/aunefyren/autotaggerr/models"
)

// The action tools go through the same commands as the REST routes, so these tests
// are about what the MCP layer adds — argument handling, the shapes, and that each
// guard the command applies reaches the assistant as a readable refusal.

func TestMCPVerbTools(t *testing.T) {
	_, api := setupAPI(t)
	artistFixture(t, api.DB, "art-1", "Band", models.ManagedByAutotaggerr, false)
	session := mcpSession(t, api, mcpWriter)

	// Nothing indexed: each scope refuses rather than queueing a run that does nothing.
	expectMCPError(t, session, "tag_files", nil, "process a library first")
	expectMCPError(t, session, "tag_files", map[string]any{"artist_mbid": "art-1"}, "process them first")
	expectMCPError(t, session, "process", map[string]any{"artist_mbid": "art-1"}, "")
	expectMCPError(t, session, "process", map[string]any{"artist_mbid": "nobody"}, "artist not found")
	expectMCPError(t, session, "process", map[string]any{"library_id": "nope"}, "get_configuration")
	expectMCPError(t, session, "process", map[string]any{"library_id": "0190a1b2-0000-7000-8000-000000000000", "artist_mbid": "art-1"}, "not both")
	expectMCPError(t, session, "refresh_metadata", map[string]any{"force": true}, "one artist only")
	expectMCPError(t, session, "cancel_job", nil, "nothing is running")

	// A metadata refresh for one artist queues, and says where to watch it.
	queued := callMCP[mcpQueued](t, session, "refresh_metadata", map[string]any{"artist_mbid": "art-1"})
	if queued.Status != "refresh queued" || queued.Artist != "Band" || queued.Follow == "" {
		t.Errorf("refresh_metadata = %+v", queued)
	}

	// Scan answers inline at both scopes. The artist first: the collection-wide pass
	// retires an artist who owns nothing, which this fixture's does not.
	artistScan := callMCP[mcpScanResult](t, session, "scan", map[string]any{"artist_mbid": "art-1"})
	if artistScan.Artist != "Band" || !artistScan.Completed {
		t.Errorf("artist scan = %+v", artistScan)
	}
	scan := callMCP[mcpScanResult](t, session, "scan", nil)
	if scan.Queued || !scan.Completed {
		t.Errorf("scan = %+v, want an inline result", scan)
	}
}

func TestMCPTagFilesQueues(t *testing.T) {
	_, api := setupAPI(t)
	artistFixture(t, api.DB, "art-1", "Band", models.ManagedByAutotaggerr, false)
	profile := models.TaggerProfile{Name: "no-write", WriteTags: false}
	if err := api.DB.Create(&profile).Error; err != nil {
		t.Fatalf("profile: %v", err)
	}
	api.DB.Model(&profile).Update("write_tags", false)
	lib := models.Library{Name: "L", Path: "/m", Enabled: true, TaggerProfileID: &profile.ID}
	if err := api.DB.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	if err := api.DB.Create(&models.CollectionRelease{MBID: "rel-x", ReleaseGroupMBID: "rg-1", ArtistMBID: "art-1"}).Error; err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := api.DB.Create(&models.LibraryItem{
		LibraryID: lib.ID, Path: "/m/Band/A/01.flac", MBReleaseID: "rel-x", Status: models.LibraryItemStatusOK,
	}).Error; err != nil {
		t.Fatalf("item: %v", err)
	}
	session := mcpSession(t, api, mcpWriter)

	queued := callMCP[mcpQueued](t, session, "tag_files", map[string]any{"library_id": lib.ID.String()})
	if queued.Status != "tagging queued" || queued.Library != "L" || queued.Files != 1 {
		t.Errorf("tag_files = %+v, want one file in library L", queued)
	}
}

func TestMCPCollectionEdits(t *testing.T) {
	_, api := setupAPI(t)
	artistFixture(t, api.DB, "lidarr-1", "Managed", models.ManagedByLidarr, true)
	api.Meta = &fakeMeta{
		getArtist: func(id string) (models.MusicBrainzArtistLookup, error) {
			if id != "art-new" {
				return models.MusicBrainzArtistLookup{}, errors.New("404")
			}
			return models.MusicBrainzArtistLookup{ID: id, Name: "Talk Talk"}, nil
		},
		getArtistRGs: func(id string) ([]models.MusicBrainzArtistReleaseGroup, bool, error) {
			return []models.MusicBrainzArtistReleaseGroup{
				{ID: "rg-spirit", Title: "Spirit of Eden", PrimaryType: "Album", FirstReleaseDate: "1988-09-12"},
				{ID: "rg-laughing", Title: "Laughing Stock", PrimaryType: "Album", FirstReleaseDate: "1991-09-16"},
			}, true, nil
		},
	}
	session := mcpSession(t, api, mcpWriter)

	added := callMCP[mcpAddArtistResult](t, session, "add_artist", map[string]any{"mbid": "art-new"})
	if added.Name != "Talk Talk" || added.AlreadyInCollection {
		t.Fatalf("add_artist = %+v, want Talk Talk looked up and added", added)
	}
	again := callMCP[mcpAddArtistResult](t, session, "add_artist", map[string]any{"mbid": "art-new"})
	if !again.AlreadyInCollection {
		t.Errorf("second add_artist = %+v, want already_in_collection", again)
	}
	expectMCPError(t, session, "add_artist", map[string]any{"mbid": "unknown"}, "search_artists")

	// An album the collection has not seen is filled in from the discography.
	want := callMCP[mcpWantResult](t, session, "want_album", map[string]any{"artist_mbid": "art-new", "album_mbid": "rg-laughing"})
	if !want.Wanted || want.Title != "Laughing Stock" {
		t.Fatalf("want_album = %+v", want)
	}
	detail := callMCP[mcpArtistDetail](t, session, "get_artist", map[string]any{"mbid": "art-new"})
	if len(detail.Albums) != 1 || !detail.Albums[0].Wanted || detail.Albums[0].WantedSource != "explicit" {
		t.Errorf("after want_album, albums = %+v; want Laughing Stock wanted explicitly", detail.Albums)
	}
	expectMCPError(t, session, "want_album", map[string]any{"artist_mbid": "art-new", "album_mbid": "rg-nope"}, "browse_artist_albums")
	expectMCPError(t, session, "want_album", map[string]any{"artist_mbid": "nobody", "album_mbid": "rg-spirit"}, "add_artist first")
	// The Lidarr gate reaches the assistant as the same refusal the UI gets.
	releaseGroupFixture(t, api.DB, "lidarr-1", rgFixture{mbid: "rg-managed", title: "Managed Album", primary: "Album"})
	expectMCPError(t, session, "want_album", map[string]any{"artist_mbid": "lidarr-1", "album_mbid": "rg-managed"}, "managed by Lidarr")

	cleared := callMCP[mcpWantResult](t, session, "unwant_album", map[string]any{"album_mbid": "rg-laughing"})
	if cleared.Wanted {
		t.Errorf("unwant_album = %+v", cleared)
	}
	detail = callMCP[mcpArtistDetail](t, session, "get_artist", map[string]any{"mbid": "art-new"})
	if len(detail.Albums) != 1 || detail.Albums[0].Wanted {
		t.Errorf("after unwant_album, albums = %+v; want nothing wanted", detail.Albums)
	}

	// Following syncs the discography and reports what it now wants.
	follow := callMCP[mcpFollowResult](t, session, "follow_artist", map[string]any{"mbid": "art-new", "monitored": true, "follow_types": "Album"})
	if !follow.Monitored || !follow.FollowGoverns || follow.Wanted != 2 {
		t.Errorf("follow_artist = %+v, want following with both albums wanted", follow)
	}
	expectMCPError(t, session, "follow_artist", map[string]any{"mbid": "art-new", "follow_from_year": 3000}, "follow_from_year")
	expectMCPError(t, session, "follow_artist", map[string]any{"mbid": "nobody", "monitored": true}, "list_artists")
}

func TestMCPFileIdentityTools(t *testing.T) {
	_, api := setupAPI(t)
	item := seedAttachFixtures(t, api.DB)
	session := mcpSession(t, api, mcpWriter)

	expectMCPError(t, session, "attach_file", map[string]any{"file_id": "nope", "release_mbid": "rel-1", "track_mbid": "t1"}, "list_files")
	expectMCPError(t, session, "attach_file", map[string]any{"file_id": item.ID.String(), "release_mbid": "rel-1", "track_mbid": "t9"}, "not part of that release")

	attached := callMCP[mcpTagOutcome](t, session, "attach_file", map[string]any{"file_id": item.ID.String(), "release_mbid": "rel-1", "track_mbid": "t1"})
	if !attached.Done {
		t.Fatalf("attach_file = %+v", attached)
	}
	var stored models.LibraryItem
	api.DB.First(&stored, "id = ?", item.ID)
	if !stored.Pinned || stored.MBReleaseTrackID != "t1" || stored.Status != models.LibraryItemStatusOK {
		t.Errorf("after attach_file, item = pinned %v track %q status %q", stored.Pinned, stored.MBReleaseTrackID, stored.Status)
	}

	expectMCPError(t, session, "confirm_pin", map[string]any{"file_id": item.ID.String()}, "no held pin")

	released := callMCP[mcpTagOutcome](t, session, "release_pin", map[string]any{"file_id": item.ID.String()})
	api.DB.First(&stored, "id = ?", item.ID)
	if !released.Done || stored.Pinned {
		t.Errorf("release_pin = %+v, pinned after = %v", released, stored.Pinned)
	}
	expectMCPError(t, session, "release_pin", map[string]any{"file_id": "0190a1b2-0000-7000-8000-000000000000"}, "not found")
}

func TestMCPMigrationDecisions(t *testing.T) {
	_, api := setupAPI(t)
	row := models.MusicbrainzMigration{
		EntityType: "release", OldMBID: "rel-old", NewMBID: "rel-new", Kind: "merge",
		Status: models.MigrationStatusPending, Name: "Old Pressing", DetectedAt: time.Now(),
	}
	if err := api.DB.Create(&row).Error; err != nil {
		t.Fatalf("migration: %v", err)
	}
	session := mcpSession(t, api, mcpWriter)

	listed := callMCP[mcpMigrationList](t, session, "list_migrations", map[string]any{"status": "open"})
	if listed.Total != 1 {
		t.Fatalf("list_migrations = %+v, want the pending row", listed)
	}
	dismissed := callMCP[mcpMigrationDecision](t, session, "dismiss_migration", map[string]any{"id": row.ID.String()})
	if dismissed.Status != models.MigrationStatusDismissed {
		t.Errorf("dismiss_migration = %+v", dismissed)
	}
	// A dismissal can be reconsidered; an applied change cannot be applied again.
	approved := callMCP[mcpMigrationDecision](t, session, "approve_migration", map[string]any{"id": row.ID.String()})
	// Nothing points at the old release, so it settles as resolved rather than applied.
	if (approved.Status != models.MigrationStatusApplied && approved.Status != models.MigrationStatusResolved) || approved.Queued {
		t.Errorf("approve_migration = %+v, want applied or resolved", approved)
	}
	expectMCPError(t, session, "approve_migration", map[string]any{"id": row.ID.String()}, "")
	expectMCPError(t, session, "approve_migration", map[string]any{"id": "nope"}, "list_migrations")
}

func TestMCPSearchTools(t *testing.T) {
	_, api := setupAPI(t)
	artistFixture(t, api.DB, "art-1", "Kate Bush", models.ManagedByAutotaggerr, false)
	api.Meta = &fakeMeta{
		searchArtists: func(q string) ([]models.MusicBrainzArtistSearchResult, error) {
			return []models.MusicBrainzArtistSearchResult{
				{ID: "art-1", Name: "Kate Bush", Score: 100},
				{ID: "art-2", Name: "Kate Bush Tribute", Score: 60},
			}, nil
		},
		searchRel: func(q metadata.ReleaseSearchQuery) (metadata.ReleaseSearchPage, error) {
			if q.Release != "Aerial" || q.Limit != 2 {
				t.Errorf("search query = %+v, want title Aerial, limit 2", q)
			}
			hit := models.MusicBrainzReleaseSearchResult{ID: "rel-1", Title: "Aerial", Score: 100,
				ArtistCredit: []models.ArtistCredit{{Name: "Kate Bush"}}}
			hit.ReleaseGroup.ID = "rg-1"
			return metadata.ReleaseSearchPage{Count: 3, Offset: 0, Releases: []models.MusicBrainzReleaseSearchResult{hit, hit}}, nil
		},
	}
	session := mcpSession(t, api, mcpWriter)

	artists := callMCP[mcpArtistHits](t, session, "search_artists", map[string]any{"query": "kate bush"})
	if len(artists.Artists) != 2 || !artists.Artists[0].InCollection || artists.Artists[1].InCollection {
		t.Errorf("search_artists = %+v, want the first marked in the collection", artists)
	}
	expectMCPError(t, session, "search_artists", map[string]any{"query": " "}, "query is required")

	releases := callMCP[mcpReleaseHits](t, session, "search_releases", map[string]any{"title": "Aerial", "limit": 2})
	if len(releases.Releases) != 2 || releases.NextOffset == nil || releases.Releases[0].AlbumMBID != "rg-1" || releases.Releases[0].Artist != "Kate Bush" {
		t.Errorf("search_releases = %+v", releases)
	}
	expectMCPError(t, session, "search_releases", nil, "at least one")
}
