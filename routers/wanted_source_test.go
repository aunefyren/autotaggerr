package routers

import (
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// The wanted-source rules decide what the artist page shows *and* whether the row
// offers a control for it. They are pure, so they are tested directly rather than
// through HTTP — a mis-derived source is a UI that claims a toggle it does not have.

func album(mbid string) models.CollectionReleaseGroup {
	return models.CollectionReleaseGroup{MBID: mbid, PrimaryType: "Album"}
}

// manualWants builds hand-authored wants for the editions named; an empty string is
// the "any edition" want.
func manualWants(releaseMBIDs ...string) []models.CollectionDesire {
	out := make([]models.CollectionDesire, 0, len(releaseMBIDs))
	for _, id := range releaseMBIDs {
		out = append(out, models.CollectionDesire{
			ReleaseGroupMBID: "rg1", ReleaseMBID: id, Source: models.DesireSourceManual,
		})
	}
	return out
}

// TestAutoWantRequiresFollowing is the guard for the reported bug: an album must
// never be reported as automatically wanted unless the artist is actually followed.
func TestAutoWantRequiresFollowing(t *testing.T) {
	artist := models.CollectionArtist{MBID: "a1", ManagedBy: models.ManagedByAutotaggerr, Monitored: false}

	view := newReleaseGroupView(album("rg1"), false, artist, nil)
	if view.Wanted || view.WantedSource != "" {
		t.Errorf("unfollowed artist produced wanted=%v source=%q", view.Wanted, view.WantedSource)
	}

	artist.Monitored = true
	view = newReleaseGroupView(album("rg1"), false, artist, nil)
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

	view := newReleaseGroupView(album("rg1"), true, artist, nil)
	if view.WantedSource == wantedSourceAuto {
		t.Error("a Lidarr-managed artist reported a native follow as the reason an album is wanted")
	}
	if view.Wanted {
		t.Errorf("album Lidarr does not monitor was reported wanted: %+v", view)
	}

	// Mixed artists have at least one Lidarr-managed library, so the same holds.
	artist.ManagedBy = models.ManagedByMixed
	if view := newReleaseGroupView(album("rg1"), true, artist, nil); view.WantedSource == wantedSourceAuto {
		t.Error("a mixed-managed artist reported an auto want")
	}
}

// TestManagerMonitoringIsTheWantedSourceForManagedArtists: for a Lidarr artist the
// honest answer to "why is this wanted" is Lidarr's own monitored flag.
func TestManagerMonitoringIsTheWantedSourceForManagedArtists(t *testing.T) {
	artist := models.CollectionArtist{MBID: "a1", ManagedBy: models.ManagedByLidarr}
	rg := album("rg1")
	rg.InCatalog, rg.CatalogMonitored = true, true

	view := newReleaseGroupView(rg, true, artist, nil)
	if !view.Wanted || view.WantedSource != wantedSourceManager {
		t.Errorf("wanted=%v source=%q, want manager", view.Wanted, view.WantedSource)
	}

	// Monitored in the catalog but not actually in it is not a want.
	rg.InCatalog = false
	if view := newReleaseGroupView(rg, true, artist, nil); view.Wanted {
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
		anyEdition := newReleaseGroupView(album("rg1"), true, artist, manualWants(""))
		if anyEdition.WantedSource != wantedSourceExplicit {
			t.Errorf("any-edition want under %s: source = %q", artist.ManagedBy, anyEdition.WantedSource)
		}
		editions := newReleaseGroupView(album("rg1"), true, artist, manualWants("rel-1"))
		if editions.WantedSource != wantedSourceExplicit {
			t.Errorf("specific-edition want under %s: source = %q", artist.ManagedBy, editions.WantedSource)
		}
	}
}

// TestManagerDesireIsReportedAsManagerState: the edition Lidarr selected reaches the
// page as a wanted edition — the reported gap was an album green on a release with
// nothing marked wanted — but it is *not* an explicit pick, because the user cannot
// unpick it. A toggle whose off direction silently does nothing is worse than a
// disabled one.
func TestManagerDesireIsReportedAsManagerState(t *testing.T) {
	artist := models.CollectionArtist{MBID: "a1", ManagedBy: models.ManagedByLidarr}
	rg := album("rg1")
	rg.InCatalog, rg.CatalogMonitored, rg.CatalogReleaseMBID = true, true, "rel-1"

	view := newReleaseGroupView(rg, true, artist, []models.CollectionDesire{{
		ReleaseGroupMBID: "rg1", ReleaseMBID: "rel-1", Source: models.DesireSourceManager,
	}})

	if !view.Wanted || view.WantedSource != wantedSourceManager {
		t.Errorf("wanted=%v source=%q, want manager", view.Wanted, view.WantedSource)
	}
	if len(view.DesiredReleases) != 1 || view.DesiredReleases[0] != "rel-1" {
		t.Errorf("desired releases = %v, want the monitored edition", view.DesiredReleases)
	}
	// The manager chose an edition, so this is no longer an "any edition" want —
	// which is the whole point: the want narrowed from the album to the release.
	if view.WantedAnyEdition {
		t.Error("a selected edition was still reported as any-edition")
	}
	if view.IdentityEditable {
		t.Error("a Lidarr artist's row reported its identity as editable")
	}
}

// TestAutoDesireIsReportedAsAutoState: an auto want descends from an explicit one, so
// the album stays wanted — but the rebuild maintains which edition it names, so it is
// state and not a toggle. Reporting it as explicit offered an unpick control that did
// not do what it said.
func TestAutoDesireIsReportedAsAutoState(t *testing.T) {
	artist := models.CollectionArtist{MBID: "a1", ManagedBy: models.ManagedByAutotaggerr, Monitored: false}

	view := newReleaseGroupView(album("rg1"), false, artist, []models.CollectionDesire{{
		ReleaseGroupMBID: "rg1", ReleaseMBID: "rel-1", Source: models.DesireSourceAuto,
	}})

	if !view.Wanted || view.WantedSource != wantedSourceAuto {
		t.Errorf("wanted=%v source=%q, want auto", view.Wanted, view.WantedSource)
	}
	if len(view.DesiredReleases) != 1 || view.DesiredReleases[0] != "rel-1" {
		t.Errorf("desired releases = %v, want the owned edition", view.DesiredReleases)
	}
}

// TestASourcelessDesireIsTreatedAsAuthored: rows predating provenance are backfilled
// at startup, but the view must not depend on that having run — an unlabelled want is
// read as the user's, which is the reading that cannot lose intent.
func TestASourcelessDesireIsTreatedAsAuthored(t *testing.T) {
	artist := models.CollectionArtist{MBID: "a1", ManagedBy: models.ManagedByAutotaggerr}

	view := newReleaseGroupView(album("rg1"), false, artist, []models.CollectionDesire{{
		ReleaseGroupMBID: "rg1", ReleaseMBID: "rel-1",
	}})

	if view.WantedSource != wantedSourceExplicit {
		t.Errorf("source = %q, want explicit", view.WantedSource)
	}
}

// TestDesireRecordingsSurviveTheView: a want for specific songs is carried across
// every desire row of the group, whichever edition each names. It was dropped by one
// of three callers before the rows themselves became the argument.
func TestDesireRecordingsSurviveTheView(t *testing.T) {
	artist := models.CollectionArtist{MBID: "a1", ManagedBy: models.ManagedByAutotaggerr}

	view := newReleaseGroupView(album("rg1"), false, artist, []models.CollectionDesire{
		{ReleaseGroupMBID: "rg1", ReleaseMBID: "rel-1", RecordingMBIDs: []string{"rec-1"}, Source: models.DesireSourceManual},
		{ReleaseGroupMBID: "rg1", ReleaseMBID: "rel-2", RecordingMBIDs: []string{"rec-2"}, Source: models.DesireSourceManual},
	})

	if len(view.DesiredRecordings) != 2 {
		t.Errorf("desired recordings = %v, want both songs", view.DesiredRecordings)
	}
	if len(view.DesiredReleases) != 2 {
		t.Errorf("desired releases = %v, want both editions", view.DesiredReleases)
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

// TestDiscrepancyNeedsAnAnsweredQuestion: "not in Lidarr" is a claim about what the
// manager said, so it may only be made once the manager has actually been asked. The
// signal used to be "does any of this artist's albums carry catalog state", which let
// an album be reported as absent from a catalogue nothing had ever put it to — the
// production case where a release-group filed under the wrong artist warned about an
// album Lidarr had all along, under a different artist.
func TestDiscrepancyNeedsAnAnsweredQuestion(t *testing.T) {
	owned := album("rg1")
	owned.Owned, owned.OwnedTracks, owned.TotalTracks = true, 12, 12
	artist := models.CollectionArtist{MBID: "a1", ManagedBy: models.ManagedByLidarr}

	// Never synced: nothing to compare against, so no claim is made.
	if view := newReleaseGroupView(owned, false, artist, nil); view.Discrepancy != models.DiscrepancyNone {
		t.Errorf("unsynced artist reported %q; absence of an answer is not a negative answer", view.Discrepancy)
	}

	// Once the manager has answered and has no album for it, the warning is earned.
	if view := newReleaseGroupView(owned, true, artist, nil); view.Discrepancy != models.DiscrepancyUnmapped {
		t.Errorf("synced artist with no catalog album reported %q, want unmapped", view.Discrepancy)
	}
}

// TestCatalogCheckedMap: the overview derives the same per-artist answer as the
// detail pages, so a mismatch count can never disagree with the album row it counted.
func TestCatalogCheckedMap(t *testing.T) {
	now := time.Now()
	checked := catalogChecked([]models.CollectionArtist{
		{MBID: "synced", LastSyncedAt: &now},
		{MBID: "unsynced"},
	})
	if !checked["synced"] {
		t.Error("a synced artist should be comparable")
	}
	if checked["unsynced"] {
		t.Error("an unsynced artist should not be")
	}
	if checked["unknown"] {
		t.Error("an artist absent from the list must default to not-comparable")
	}
}
