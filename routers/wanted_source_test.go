package routers

import (
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// The wanted-source rules decide what the artist page shows *and* whether the row
// offers a control for it. They are pure, so they are tested directly rather than
// through HTTP — a mis-derived source is a UI that claims a toggle it does not have.

func album(mbid string) models.CollectionReleaseGroup {
	return models.CollectionReleaseGroup{MBID: mbid, PrimaryType: "Album"}
}

// TestAutoWantRequiresFollowing is the guard for the reported bug: an album must
// never be reported as automatically wanted unless the artist is actually followed.
func TestAutoWantRequiresFollowing(t *testing.T) {
	artist := models.CollectionArtist{MBID: "a1", ManagedBy: models.ManagedByAutotaggerr, Monitored: false}

	view := newReleaseGroupView(album("rg1"), false, false, artist, nil, nil)
	if view.Wanted || view.WantedSource != "" {
		t.Errorf("unfollowed artist produced wanted=%v source=%q", view.Wanted, view.WantedSource)
	}

	artist.Monitored = true
	view = newReleaseGroupView(album("rg1"), false, false, artist, nil, nil)
	if !view.Wanted || view.WantedSource != wantedSourceAuto {
		t.Errorf("followed artist: wanted=%v source=%q, want auto", view.Wanted, view.WantedSource)
	}
}

// TestFollowDoesNotGovernManagedArtists: a native follow flag left over from before
// an artist became Lidarr-managed used to keep producing "auto" wants on a page
// that showed no follow control at all — state with no visible cause and no way to
// turn it off. Lidarr is the authority for its own artists.
func TestFollowDoesNotGovernManagedArtists(t *testing.T) {
	artist := models.CollectionArtist{MBID: "a1", ManagedBy: models.ManagedByLidarr, Monitored: true}

	view := newReleaseGroupView(album("rg1"), true, false, artist, nil, nil)
	if view.WantedSource == wantedSourceAuto {
		t.Error("a Lidarr-managed artist reported a native follow as the reason an album is wanted")
	}
	if view.Wanted {
		t.Errorf("album Lidarr does not monitor was reported wanted: %+v", view)
	}

	// Mixed artists have at least one Lidarr-managed library, so the same holds.
	artist.ManagedBy = models.ManagedByMixed
	if view := newReleaseGroupView(album("rg1"), true, false, artist, nil, nil); view.WantedSource == wantedSourceAuto {
		t.Error("a mixed-managed artist reported an auto want")
	}
}

// TestManagerMonitoringIsTheWantedSourceForManagedArtists: for a Lidarr artist the
// honest answer to "why is this wanted" is Lidarr's own monitored flag.
func TestManagerMonitoringIsTheWantedSourceForManagedArtists(t *testing.T) {
	artist := models.CollectionArtist{MBID: "a1", ManagedBy: models.ManagedByLidarr}
	rg := album("rg1")
	rg.InCatalog, rg.CatalogMonitored = true, true

	view := newReleaseGroupView(rg, true, false, artist, nil, nil)
	if !view.Wanted || view.WantedSource != wantedSourceManager {
		t.Errorf("wanted=%v source=%q, want manager", view.Wanted, view.WantedSource)
	}

	// Monitored in the catalog but not actually in it is not a want.
	rg.InCatalog = false
	if view := newReleaseGroupView(rg, true, false, artist, nil, nil); view.Wanted {
		t.Errorf("album absent from the catalog was reported wanted: %+v", view)
	}
}

// TestExplicitWantOutranksEverything: a pick is authored intent and must survive
// unfollowing, a manager change, or the manager dropping the album.
func TestExplicitWantOutranksEverything(t *testing.T) {
	for _, artist := range []models.CollectionArtist{
		{MBID: "a1", ManagedBy: models.ManagedByAutotaggerr, Monitored: false},
		{MBID: "a1", ManagedBy: models.ManagedByAutotaggerr, Monitored: true},
		{MBID: "a1", ManagedBy: models.ManagedByLidarr, Monitored: true},
	} {
		anyEdition := newReleaseGroupView(album("rg1"), true, true, artist, nil, nil)
		if anyEdition.WantedSource != wantedSourceExplicit {
			t.Errorf("any-edition want under %s: source = %q", artist.ManagedBy, anyEdition.WantedSource)
		}
		editions := newReleaseGroupView(album("rg1"), true, false, artist, []string{"rel-1"}, nil)
		if editions.WantedSource != wantedSourceExplicit {
			t.Errorf("specific-edition want under %s: source = %q", artist.ManagedBy, editions.WantedSource)
		}
	}
}

// TestFollowGovernsIsReportedToTheUI: the UI must not re-derive who governs — that
// is how the follow panel and the row state drifted apart in the first place.
func TestFollowGovernsIsReportedToTheUI(t *testing.T) {
	cases := map[string]bool{
		models.ManagedByAutotaggerr: true,
		models.ManagedByUnknown:     true,
		models.ManagedByLidarr:      false,
		models.ManagedByMixed:       false,
	}
	for managedBy, want := range cases {
		got := newArtistView(models.CollectionArtist{ManagedBy: managedBy}).FollowGoverns
		if got != want {
			t.Errorf("FollowGoverns(%s) = %v, want %v", managedBy, got, want)
		}
	}
}
