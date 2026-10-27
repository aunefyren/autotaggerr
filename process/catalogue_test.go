package process

import (
	"errors"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/metadata"
	"github.com/aunefyren/autotaggerr/models"
)

// catalogueMeta serves a fixed discography per artist and counts the reads.
type catalogueMeta struct {
	groups map[string][]models.MusicBrainzArtistReleaseGroup
	reads  map[string]int
}

func (m *catalogueMeta) GetRelease(string) (models.MusicBrainzReleaseResponse, error) {
	return models.MusicBrainzReleaseResponse{}, errors.New("not here")
}
func (m *catalogueMeta) GetArtist(id string) (models.MusicBrainzArtistLookup, error) {
	return models.MusicBrainzArtistLookup{ID: id}, nil
}
func (m *catalogueMeta) GetArtistReleaseGroups(id string) ([]models.MusicBrainzArtistReleaseGroup, bool, error) {
	m.reads[id]++
	groups, ok := m.groups[id]
	if !ok {
		return nil, false, errors.New("MusicBrainz is unreachable")
	}
	return groups, true, nil
}
func (m *catalogueMeta) GetReleaseGroupReleases(string) ([]models.MusicBrainzReleaseSearchResult, error) {
	return nil, nil
}
func (m *catalogueMeta) SearchReleases(metadata.ReleaseSearchQuery) (metadata.ReleaseSearchPage, error) {
	return metadata.ReleaseSearchPage{}, nil
}
func (m *catalogueMeta) SearchArtists(string) ([]models.MusicBrainzArtistSearchResult, error) {
	return nil, nil
}

func catalogueRunner(t *testing.T, meta *catalogueMeta) *Runner {
	t.Helper()
	r := NewRunner(newTestDB(t), models.ConfigStruct{AutotaggerrVersion: "test"})
	r.meta = meta
	t.Cleanup(r.Wait)
	return r
}

// TestReadCataloguesReadsWhatWasNeverRead: every artist whose discography was never
// read — or was read too long ago — has it read; a followed native artist is synced,
// storing what following wants; an unfollowed one only has its catalogue counted, so
// nothing appears in its missing list.
func TestReadCataloguesReadsWhatWasNeverRead(t *testing.T) {
	two := []models.MusicBrainzArtistReleaseGroup{
		{ID: "rg-a", Title: "A", PrimaryType: "Album"},
		{ID: "rg-b", Title: "B", PrimaryType: "Album"},
	}
	meta := &catalogueMeta{
		groups: map[string][]models.MusicBrainzArtistReleaseGroup{"scanned": two, "followed": two, "lidarr": two, "stale": two},
		reads:  map[string]int{},
	}
	r := catalogueRunner(t, meta)
	recent, old := time.Now(), time.Now().Add(-2*catalogueMaxAge)
	for _, a := range []models.CollectionArtist{
		{MBID: "scanned", Name: "Scanned", ManagedBy: models.ManagedByAutotaggerr},
		{MBID: "followed", Name: "Followed", ManagedBy: models.ManagedByAutotaggerr, Monitored: true},
		{MBID: "lidarr", Name: "Lidarr", ManagedBy: models.ManagedByLidarr, Monitored: true},
		{MBID: "fresh", Name: "Fresh", ManagedBy: models.ManagedByAutotaggerr, CatalogueReadAt: &recent, CatalogueCount: 9},
		{MBID: "stale", Name: "Stale", ManagedBy: models.ManagedByAutotaggerr, CatalogueReadAt: &old},
		{MBID: "down", Name: "Down", ManagedBy: models.ManagedByAutotaggerr},
	} {
		if err := r.db.Create(&a).Error; err != nil {
			t.Fatal(err)
		}
	}

	r.ReadCatalogues()
	r.waitIdle(t)

	artist := func(mbid string) models.CollectionArtist {
		var a models.CollectionArtist
		r.db.Where("mb_id = ?", mbid).First(&a)
		return a
	}
	for _, mbid := range []string{"scanned", "followed", "lidarr", "stale"} {
		if a := artist(mbid); a.CatalogueCount != 2 || a.CatalogueReadAt == nil {
			t.Errorf("%s: catalogue = %d read %v, want 2", mbid, a.CatalogueCount, a.CatalogueReadAt)
		}
	}
	if meta.reads["fresh"] != 0 || artist("fresh").CatalogueCount != 9 {
		t.Error("a discography read recently was read again")
	}
	if artist("down").CatalogueReadAt != nil {
		t.Error("a failed read was recorded as read — it would never be tried again")
	}

	stored := func(mbid string) int64 {
		var n int64
		r.db.Model(&models.CollectionReleaseGroup{}).Where("artist_mb_id = ?", mbid).Count(&n)
		return n
	}
	if stored("followed") != 2 {
		t.Errorf("a followed artist's wanted albums = %d, want 2 stored", stored("followed"))
	}
	if stored("scanned") != 0 || stored("lidarr") != 0 {
		t.Error("reading a catalogue stored albums nobody follows — or wrote over a manager's catalogue")
	}

	var event models.Event
	r.db.Where("type = ?", models.EventTypeDiscographyRead).First(&event)
	if event.Status != models.EventStatusError || event.Summary == "" {
		t.Errorf("event = %s %q", event.Status, event.Summary)
	}

	// Nothing left to read records nothing.
	r.db.Where("mb_id = ?", "down").Delete(&models.CollectionArtist{})
	r.ReadCatalogues()
	r.waitIdle(t)
	var events int64
	r.db.Model(&models.Event{}).Where("type = ?", models.EventTypeDiscographyRead).Count(&events)
	if events != 1 {
		t.Errorf("events = %d, want 1 — a pass with nothing to read recorded one", events)
	}
}

func TestFollowArtistsSyncsThem(t *testing.T) {
	meta := &catalogueMeta{
		groups: map[string][]models.MusicBrainzArtistReleaseGroup{"a": {{ID: "rg", Title: "X", PrimaryType: "Album"}}},
		reads:  map[string]int{},
	}
	r := catalogueRunner(t, meta)
	r.db.Create(&models.CollectionArtist{MBID: "a", Name: "A", ManagedBy: models.ManagedByAutotaggerr, Monitored: true})
	r.FollowArtists([]string{"a"})
	r.waitIdle(t)
	var n int64
	r.db.Model(&models.CollectionReleaseGroup{}).Where("artist_mb_id = ?", "a").Count(&n)
	if n != 1 {
		t.Errorf("wanted albums after following = %d, want 1", n)
	}
}
