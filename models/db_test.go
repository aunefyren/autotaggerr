package models

import (
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The domain models are mostly field declarations, but the handful of functions on
// them encode rules the rest of the app trusts: which data source may play which
// role, and how the disk view and the catalog view are reconciled. Those are worth
// pinning here rather than only through whichever handler happens to call them.

func TestBaseBeforeCreateAssignsUUID(t *testing.T) {
	var b Base
	if err := b.BeforeCreate(nil); err != nil {
		t.Fatalf("BeforeCreate: %v", err)
	}
	if b.ID == uuid.Nil {
		t.Fatal("BeforeCreate left the ID unset")
	}
	if v := b.ID.Version(); v != 7 {
		t.Errorf("ID version = %v, want 7 (time-ordered, for index locality)", v)
	}

	// A caller-chosen ID must survive: seeds and tests assign their own.
	chosen := uuid.MustParse("00000000-0000-7000-8000-000000000abc")
	withID := Base{ID: chosen}
	if err := withID.BeforeCreate(nil); err != nil {
		t.Fatalf("BeforeCreate with an ID: %v", err)
	}
	if withID.ID != chosen {
		t.Errorf("ID = %v, want the caller's %v", withID.ID, chosen)
	}
}

func TestDataSourceCategory(t *testing.T) {
	cases := map[string]string{
		DataSourceTypeMusicBrainz:     DataSourceCategoryMetadata,
		DataSourceTypeAcoustID:        DataSourceCategoryFingerprint,
		DataSourceTypeCoverArtArchive: DataSourceCategoryArtwork,
		DataSourceTypeFanart:          DataSourceCategoryArtwork,
		"spotify":                     "", // unknown is valid for nothing, never guessed
		"":                            "",
	}
	for sourceType, want := range cases {
		if got := DataSourceCategory(sourceType); got != want {
			t.Errorf("DataSourceCategory(%q) = %q, want %q", sourceType, got, want)
		}
	}
}

func TestDataSourceIsSingleton(t *testing.T) {
	cases := map[string]bool{
		DataSourceTypeAcoustID:        true,
		DataSourceTypeCoverArtArchive: true,
		DataSourceTypeFanart:          true,
		// MusicBrainz is the deliberate exception: a local mirror alongside the public
		// service is a legitimate setup.
		DataSourceTypeMusicBrainz: false,
		"spotify":                 false,
	}
	for sourceType, want := range cases {
		if got := DataSourceIsSingleton(sourceType); got != want {
			t.Errorf("DataSourceIsSingleton(%q) = %v, want %v", sourceType, got, want)
		}
	}
}

func TestCompleteRequiresAKnownTotal(t *testing.T) {
	// A zero total means "unknown", not "nothing missing" — the case that would
	// otherwise report an album with no known tracklist as fully owned.
	if (CollectionRelease{OwnedTracks: 0, TotalTracks: 0}).Complete() {
		t.Error("release with an unknown total should not be complete")
	}
	if (CollectionRelease{OwnedTracks: 3, TotalTracks: 5}).Complete() {
		t.Error("partially owned release should not be complete")
	}
	if !(CollectionRelease{OwnedTracks: 5, TotalTracks: 5}).Complete() {
		t.Error("fully owned release should be complete")
	}
	// More files than tracks (a bonus disc, a duplicate) still counts as complete.
	if !(CollectionRelease{OwnedTracks: 6, TotalTracks: 5}).Complete() {
		t.Error("over-owned release should be complete")
	}

	if (CollectionReleaseGroup{Owned: false, OwnedTracks: 5, TotalTracks: 5}).Complete() {
		t.Error("release-group not owned on disk should not be complete")
	}
	if !(CollectionReleaseGroup{Owned: true, OwnedTracks: 5, TotalTracks: 5}).Complete() {
		t.Error("owned, fully present release-group should be complete")
	}
}

func TestReleaseGroupDiscrepancy(t *testing.T) {
	cases := []struct {
		name       string
		rg         CollectionReleaseGroup
		hasCatalog bool
		want       string
	}{
		{
			// Without a catalog there is nothing to compare against, so nothing is
			// flagged — otherwise every album of an unmonitored native artist would
			// look unmapped.
			name:       "no catalog flags nothing",
			rg:         CollectionReleaseGroup{Owned: true, OwnedTracks: 5, TotalTracks: 5},
			hasCatalog: false,
			want:       DiscrepancyNone,
		},
		{
			name:       "owned but absent from the catalog is unmapped",
			rg:         CollectionReleaseGroup{Owned: true, InCatalog: false},
			hasCatalog: true,
			want:       DiscrepancyUnmapped,
		},
		{
			// An unknown catalog total (native discovery does not count tracks) cannot
			// be compared, so it is not a discrepancy.
			name:       "unknown catalog total is not a discrepancy",
			rg:         CollectionReleaseGroup{Owned: true, InCatalog: true, OwnedTracks: 5, CatalogTotalTracks: 0},
			hasCatalog: true,
			want:       DiscrepancyNone,
		},
		{
			name:       "more on disk than the manager knows is a stale catalog",
			rg:         CollectionReleaseGroup{Owned: true, InCatalog: true, OwnedTracks: 12, CatalogOwnedTracks: 3, CatalogTotalTracks: 12},
			hasCatalog: true,
			want:       DiscrepancyStaleCatalog,
		},
		{
			name:       "more in the manager than indexed is not indexed",
			rg:         CollectionReleaseGroup{Owned: true, InCatalog: true, OwnedTracks: 3, CatalogOwnedTracks: 12, CatalogTotalTracks: 12},
			hasCatalog: true,
			want:       DiscrepancyNotIndexed,
		},
		{
			name:       "agreement is no discrepancy",
			rg:         CollectionReleaseGroup{Owned: true, InCatalog: true, OwnedTracks: 12, CatalogOwnedTracks: 12, CatalogTotalTracks: 12},
			hasCatalog: true,
			want:       DiscrepancyNone,
		},
		{
			name:       "not owned and not in catalog is no discrepancy",
			rg:         CollectionReleaseGroup{Owned: false, InCatalog: false},
			hasCatalog: true,
			want:       DiscrepancyNone,
		},
	}
	for _, c := range cases {
		if got := c.rg.Discrepancy(c.hasCatalog); got != c.want {
			t.Errorf("%s: Discrepancy(%v) = %q, want %q", c.name, c.hasCatalog, got, c.want)
		}
	}
}

// TestAllDBModelsCoversEveryTable guards the AutoMigrate set: a model added to the
// package but forgotten here gets no table, which fails at runtime rather than build
// time. It cannot detect the omission itself, so it pins the invariants it can — the
// set is non-empty and has no duplicates.
func TestAllDBModelsCoversEveryTable(t *testing.T) {
	all := AllDBModels()
	if len(all) == 0 {
		t.Fatal("AllDBModels is empty")
	}
	seen := map[string]bool{}
	for _, m := range all {
		if m == nil {
			t.Error("AllDBModels contains a nil entry")
			continue
		}
		key := reflect.TypeOf(m).String()
		if seen[key] {
			t.Errorf("%s listed twice in AllDBModels", key)
		}
		seen[key] = true
	}
}

// IsVariousArtists matches the shared placeholder MBID case-insensitively and after
// trimming, and rejects anything else — the guard that keeps a "Various Artists"
// compilation from triggering an unbounded discography pull.
func TestIsVariousArtists(t *testing.T) {
	if !IsVariousArtists(VariousArtistsMBID) {
		t.Error("the canonical VA MBID should match")
	}
	if !IsVariousArtists("  " + VariousArtistsMBID + "  ") {
		t.Error("surrounding whitespace should be trimmed before matching")
	}
	if !IsVariousArtists(strings.ToUpper(VariousArtistsMBID)) {
		t.Error("matching should be case-insensitive")
	}
	if IsVariousArtists("89ad4ac3-0000-0000-0000-000000000000") {
		t.Error("an unrelated MBID must not match")
	}
	if IsVariousArtists("") {
		t.Error("the empty string must not match")
	}
}
