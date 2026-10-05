package migration

import (
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

// closedDB returns a database whose connection has been closed, so every query on it
// fails. It stands in for a database that went away mid-run.
func closedDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return db
}

// TestNoDatabaseIsHandled: the one-shot file mode runs without a database, and every
// entry point a caller can reach there has to answer rather than panic.
func TestNoDatabaseIsHandled(t *testing.T) {
	if _, err := ProcessPending(nil, Policy{}); err == nil {
		t.Error("ProcessPending(nil) should report that there is no database")
	}
	if _, _, err := List(nil, ListOptions{}); err == nil {
		t.Error("List(nil) should report that there is no database")
	}
	if n, err := MarkRepairQueued(nil, "artist"); n != 0 || err != nil {
		t.Errorf("MarkRepairQueued(nil) = %d, %v; want a silent no-op", n, err)
	}
	if n, err := MarkRepairQueued(testDB(t), ""); n != 0 || err != nil {
		t.Errorf("MarkRepairQueued with no artist = %d, %v; want a silent no-op", n, err)
	}
	if err := ClearRepairQueued(nil, "artist"); err != nil {
		t.Errorf("ClearRepairQueued(nil) = %v", err)
	}
	ReconcileQueued(nil)
	if moved, err := RelinkRelease(nil, "rel", "rg"); moved || err != nil {
		t.Errorf("RelinkRelease(nil) = %v, %v; want a silent no-op", moved, err)
	}

	// Without a database a review still says what the row means — it just cannot
	// say what is on disk.
	reviews := Reviews(nil, []models.MusicbrainzMigration{{
		EntityType: models.MigrationEntityArtist,
		Kind:       models.MigrationKindDeleted,
		OldMBID:    "artist",
	}})
	if len(reviews) != 1 || reviews[0].Problem == "" || reviews[0].Effect == "" {
		t.Errorf("reviews without a database = %+v, want sentences", reviews)
	}
}

// TestReviewSentencesCoverEveryKind reads the four non-album cases against each
// other: a deletion and a merge of each entity type must each say something distinct
// and accurate about what approving does.
func TestReviewSentencesCoverEveryKind(t *testing.T) {
	cases := []struct {
		name          string
		m             models.MusicbrainzMigration
		problem       string
		effectContain []string
	}{
		{
			name:          "deleted artist",
			m:             models.MusicbrainzMigration{EntityType: models.MigrationEntityArtist, Kind: models.MigrationKindDeleted},
			problem:       "no longer has this artist",
			effectContain: []string{"removes the artist"},
		},
		{
			name:          "deleted release",
			m:             models.MusicbrainzMigration{EntityType: models.MigrationEntityRelease, Kind: models.MigrationKindDeleted, AffectedFiles: 1},
			problem:       "no longer has this release",
			effectContain: []string{"re-identification"},
		},
		{
			name:          "merged artist",
			m:             models.MusicbrainzMigration{EntityType: models.MigrationEntityArtist, Kind: models.MigrationKindRedirect},
			problem:       "merged this artist",
			effectContain: []string{"No files are touched"},
		},
		{
			name: "merged release with wants and a pin",
			m: models.MusicbrainzMigration{
				EntityType: models.MigrationEntityRelease, Kind: models.MigrationKindRedirect,
				AffectedFiles: 3, AffectedDesires: 1, TouchesPinned: true,
			},
			problem:       "merged this release",
			effectContain: []string{"3 files", "1 want follows", "the pin follows it"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := NewReview(nil, c.m)
			if !strings.Contains(r.Problem, c.problem) {
				t.Errorf("problem = %q, want it to contain %q", r.Problem, c.problem)
			}
			for _, want := range c.effectContain {
				if !strings.Contains(r.Effect, want) {
					t.Errorf("effect = %q, want it to contain %q", r.Effect, want)
				}
			}
		})
	}
}

// TestReviewOfAnArtistCountsTheirFiles: an artist row has no files of its own, so the
// count goes through every edition credited to them.
func TestReviewOfAnArtistCountsTheirFiles(t *testing.T) {
	db := testDB(t)
	ownFiles(t, db, "rel-a", "rg-1", "artist-1", 2)
	ownFiles(t, db, "rel-b", "rg-2", "artist-1", 1)
	m := pendingRedirect(t, db, models.MigrationEntityArtist, "artist-1", "artist-2")
	m.Name = "Some Band"

	r := NewReview(db, m)
	if r.FilesOnDisk != 3 {
		t.Errorf("FilesOnDisk = %d, want 3", r.FilesOnDisk)
	}
	if r.ArtistMBID != "artist-1" || r.ArtistName != "Some Band" {
		t.Errorf("artist = %q/%q, want the row's own", r.ArtistMBID, r.ArtistName)
	}

	// An artist with no editions has nothing on disk.
	if got := NewReview(db, pendingRedirect(t, db, models.MigrationEntityArtist, "artist-x", "artist-y")).FilesOnDisk; got != 0 {
		t.Errorf("FilesOnDisk for an artist with no editions = %d, want 0", got)
	}
}

// TestReviewOfAReleaseNamesItsArtist: a release row names the artist through its
// edition row, and an edition with no credited artist names nobody.
func TestReviewOfAReleaseNamesItsArtist(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionArtist{MBID: "artist-1", Name: "Some Band"}).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	ownFiles(t, db, "rel-a", "rg-1", "artist-1", 1)
	ownFiles(t, db, "rel-b", "rg-2", "", 1)

	r := NewReview(db, pendingRedirect(t, db, models.MigrationEntityRelease, "rel-a", "rel-new"))
	if r.ArtistMBID != "artist-1" || r.ArtistName != "Some Band" {
		t.Errorf("artist = %q/%q, want artist-1/Some Band", r.ArtistMBID, r.ArtistName)
	}

	r = NewReview(db, pendingRedirect(t, db, models.MigrationEntityRelease, "rel-b", "rel-new"))
	if r.ArtistName != "" {
		t.Errorf("an uncredited edition named %q", r.ArtistName)
	}
}

// TestReviewsSurviveAFailingDatabase: every lookup in a review is best-effort, so a
// database that has gone away costs the counts, never the row.
func TestReviewsSurviveAFailingDatabase(t *testing.T) {
	db := closedDB(t)
	rows := []models.MusicbrainzMigration{
		{EntityType: models.MigrationEntityReleaseGroup, Kind: models.MigrationKindDeleted, OldMBID: "rg"},
		{EntityType: models.MigrationEntityArtist, Kind: models.MigrationKindRedirect, OldMBID: "a", NewMBID: "b"},
		{EntityType: models.MigrationEntityRelease, Kind: models.MigrationKindRedirect, OldMBID: "r", NewMBID: "s"},
	}
	reviews := Reviews(db, rows)
	if len(reviews) != len(rows) {
		t.Fatalf("got %d reviews, want %d", len(reviews), len(rows))
	}
	for _, r := range reviews {
		if r.Problem == "" || r.FilesOnDisk != 0 || r.ArtistOpen != 0 {
			t.Errorf("review %s = %+v, want sentences and zero counts", r.OldMBID, r)
		}
	}
	if n := countRows(db, &models.LibraryItem{}, "1 = 1"); n != 0 {
		t.Errorf("countRows on a closed database = %d, want 0", n)
	}
}

// TestFailingDatabaseIsReported: the entry points that are not best-effort return the
// error instead of an empty result that would read as "nothing to do".
func TestFailingDatabaseIsReported(t *testing.T) {
	db := closedDB(t)
	if _, err := ProcessPending(db, Policy{}); err == nil {
		t.Error("ProcessPending on a closed database should fail")
	}
	if _, _, err := List(db, ListOptions{}); err == nil {
		t.Error("List on a closed database should fail")
	}
	if _, err := RelinkRelease(db, "rel", "rg"); err == nil {
		t.Error("RelinkRelease on a closed database should fail")
	}
	ReconcileQueued(db) // logs, must not panic
}

func TestOrderClause(t *testing.T) {
	cases := []struct {
		opts ListOptions
		want string
	}{
		{ListOptions{}, "detected_at desc"},
		{ListOptions{Dir: "ASC"}, "detected_at asc"},
		{ListOptions{Status: StatusOpen}, "detected_at desc"},
		{ListOptions{Status: StatusClosed}, resolvedAtExpr + " desc, detected_at desc"},
		{ListOptions{Sort: ListSortName, Dir: "asc"}, "name COLLATE NOCASE asc, detected_at desc"},
		{ListOptions{Sort: ListSortEntity}, "entity_type desc, name COLLATE NOCASE asc, detected_at desc"},
		{ListOptions{Sort: ListSortStatus, Dir: "asc"}, "status asc, detected_at desc"},
		{ListOptions{Sort: "bogus"}, "detected_at desc"},
	}
	for _, c := range cases {
		if got := orderClause(c.opts); got != c.want {
			t.Errorf("orderClause(%+v) = %q, want %q", c.opts, got, c.want)
		}
	}
}

// TestListSortsByEveryColumn runs each ordering against SQLite, so a typo in a column
// name fails here rather than on the page.
func TestListSortsByEveryColumn(t *testing.T) {
	db := testDB(t)
	a := pendingRedirect(t, db, models.MigrationEntityArtist, "a-old", "a-new")
	a.Name = "beta"
	db.Save(&a)
	b := pendingDeletion(t, db, models.MigrationEntityRelease, "r-old")
	b.Name = "Alpha"
	db.Save(&b)

	for _, sort := range []string{ListSortDetected, ListSortResolved, ListSortName, ListSortEntity, ListSortStatus} {
		rows, total, err := List(db, ListOptions{Sort: sort, Dir: "asc"})
		if err != nil {
			t.Fatalf("List sorted by %s: %v", sort, err)
		}
		if total != 2 || len(rows) != 2 {
			t.Errorf("sorted by %s: %d rows of %d, want 2", sort, len(rows), total)
		}
	}
	rows, _, _ := List(db, ListOptions{Sort: ListSortName, Dir: "asc"})
	if rows[0].Name != "Alpha" {
		t.Errorf("name order put %q first, want Alpha (case-insensitive)", rows[0].Name)
	}
}

// TestSettledRowsRefuseASecondDecision: applied and resolved rows are history.
func TestSettledRowsRefuseASecondDecision(t *testing.T) {
	db := testDB(t)
	for _, status := range []string{models.MigrationStatusApplied, models.MigrationStatusResolved} {
		m := pendingDeletion(t, db, models.MigrationEntityRelease, "rel-"+status)
		m.Status = status
		if err := db.Save(&m).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := Dismiss(db, m.ID); err == nil {
			t.Errorf("a %s row was dismissed", status)
		}
	}
	if err := settled(models.MusicbrainzMigration{Status: models.MigrationStatusFailed}); err != nil {
		t.Errorf("a failed row is still open, got %v", err)
	}
}

// TestUnreferencedArtistMergeClosesItself: an artist merge nothing in the collection
// points at has nothing to apply, so it is resolved rather than left in the queue.
func TestUnreferencedArtistMergeClosesItself(t *testing.T) {
	db := testDB(t)
	m := pendingRedirect(t, db, models.MigrationEntityArtist, "art-gone", "art-new")

	if _, err := ProcessPending(db, Policy{}); err != nil {
		t.Fatalf("ProcessPending: %v", err)
	}
	row := reload(t, db, m.ID)
	if row.Status != models.MigrationStatusResolved {
		t.Errorf("status = %q, want resolved", row.Status)
	}
	if !strings.Contains(row.ResolutionDetail, "artist ID") {
		t.Errorf("detail = %q, want the artist reason", row.ResolutionDetail)
	}
}

// TestArtistRedirectWithoutTargetFails mirrors the release case: a malformed artist
// merge is failed visibly rather than closed as moot.
func TestArtistRedirectWithoutTargetFails(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionArtist{MBID: "art-old", Name: "Old"}).Error; err != nil {
		t.Fatal(err)
	}
	m := pendingRedirect(t, db, models.MigrationEntityArtist, "art-old", "")
	if _, err := ApplyByID(db, m.ID); err == nil {
		t.Fatal("an artist redirect with no target was applied")
	}
	if row := reload(t, db, m.ID); row.Status != models.MigrationStatusFailed {
		t.Errorf("status = %q, want failed", row.Status)
	}
}

// TestArtistRedirectKeepsTheSourceNameForAnUnnamedTarget: a target row created
// before its name was known inherits the name the collection already had.
func TestArtistRedirectKeepsTheSourceNameForAnUnnamedTarget(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionArtist{MBID: "art-old", Name: "Known Name"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.CollectionArtist{MBID: "art-new"}).Error; err != nil {
		t.Fatal(err)
	}
	m := pendingRedirect(t, db, models.MigrationEntityArtist, "art-old", "art-new")
	if _, err := ApplyByID(db, m.ID); err != nil {
		t.Fatalf("ApplyByID: %v", err)
	}
	var target models.CollectionArtist
	if err := db.Where("mb_id = ?", "art-new").First(&target).Error; err != nil {
		t.Fatalf("target: %v", err)
	}
	if target.Name != "Known Name" {
		t.Errorf("target name = %q, want the source's", target.Name)
	}
}

// TestAppliedDetailForAnAlreadyRetiredAlbum: a retirement that found nothing to
// remove says so, rather than claiming it removed the album.
func TestAppliedDetailForAnAlreadyRetiredAlbum(t *testing.T) {
	got := appliedDetail(models.MusicbrainzMigration{EntityType: models.MigrationEntityReleaseGroup}, applyCounts{})
	if !strings.Contains(got, "nothing left to remove") {
		t.Errorf("detail = %q", got)
	}
}

// TestRepairableReadsTheAlbumRow: no row means nothing to hold for, and a read error
// keeps the row queued — retiring an album on a database hiccup is the worse mistake.
func TestRepairableReadsTheAlbumRow(t *testing.T) {
	m := models.MusicbrainzMigration{EntityType: models.MigrationEntityReleaseGroup, OldMBID: "rg-missing"}
	if repairable(testDB(t), m) {
		t.Error("an album with no collection row was reported repairable")
	}
	if !repairable(closedDB(t), m) {
		t.Error("a read error must report repairable, so the row stays queued")
	}
}

// TestArtistOpenCountsOnlyAlbumRows: the sibling count is about album rows one
// manager refresh settles together; a merge sharing the page gets no count.
func TestArtistOpenCountsOnlyAlbumRows(t *testing.T) {
	db := testDB(t)
	album := ghostGroup(t, db, "rg-1", "artist-1", "One", nil)
	ghostGroup(t, db, "rg-2", "artist-1", "Two", nil)
	merge := pendingRedirect(t, db, models.MigrationEntityRelease, "rel-old", "rel-new")

	reviews := Reviews(db, []models.MusicbrainzMigration{album, merge})
	if reviews[0].ArtistOpen != 2 {
		t.Errorf("album ArtistOpen = %d, want 2", reviews[0].ArtistOpen)
	}
	if reviews[1].ArtistOpen != 0 {
		t.Errorf("merge ArtistOpen = %d, want 0", reviews[1].ArtistOpen)
	}
}
