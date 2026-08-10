// Package collection materializes the "present vs wanted" view.
//
// Two authorities write to CollectionReleaseGroup, and they are kept in separate
// column blocks so they never overwrite each other:
//
//   - Rebuild owns the *disk* view — the release-groups a library actually holds,
//     aggregated from library_items + the cached MusicBrainz releases.
//   - SyncArtist (native MusicBrainz discography) and SyncLidarr (the Lidarr mirror)
//     own the *catalog* view — what the manager says should exist, and what it
//     believes is monitored.
//
// Present is universal; wanted is manager-owned. Where the two views disagree — a
// manager reporting fewer files than are on disk because it needs a rescan, or files
// no manager has an album for — the row exposes a Discrepancy rather than one side
// silently winning.
package collection

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/metadata"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Rebuild recomputes the disk ("present") side of the collection from the index. It
// reads only cached releases, so it never triggers MusicBrainz fetches. Catalog state
// — including wanted release-groups with no files — is left untouched.
//
// It runs in a transaction because its first act is to clear the disk view wholesale
// before re-establishing it. Without one, a rebuild that failed partway — or two
// overlapping rebuilds, which the scan's end-of-run call and the manual button can
// easily produce — would leave the collection reporting that it owns less than it
// does. The window is small and the consequence is silent, which is the worst
// combination to leave to chance. The transaction also serialises concurrent
// rebuilds, since the second waits on the first's write lock rather than interleaving
// its clear with the other's re-establish.
//
// What it does *not* change: the per-row upserts below still log and carry on when a
// single row fails. That resilience is deliberate and shared with the Lidarr sync —
// one unwritable row must not abandon a whole re-derivation. So the guarantee here is
// that the clear-and-re-establish is atomic, not that every row succeeded.
func Rebuild(db *gorm.DB) (RebuildStats, error) {
	return RebuildScoped(db, RebuildScope{})
}

// RebuildScope narrows a rebuild to part of the collection. The zero value is the
// whole collection, which is what a process run and the collection-wide button ask
// for.
//
// **There is no library scope.** `owned` is a flag on the release-group, not a fact
// per library: an album with files in two libraries is one row. A pass narrowed to
// one library would have to read the other libraries' files anyway to avoid clearing
// a shared album's disk view — at which point it is the whole-collection pass with
// extra steps. An artist, by contrast, owns their release-groups outright.
type RebuildScope struct {
	// ArtistMBID limits the pass to one artist's albums: the release-groups already
	// credited to them, plus any their files turn out to belong to.
	ArtistMBID string
}

func (s RebuildScope) all() bool { return s.ArtistMBID == "" }

// RebuildScoped is Rebuild narrowed by a scope. A scoped pass reads the whole index
// exactly as a full one does — it is the *writes* that are confined, which is the
// point: re-deriving one artist must not clear the disk view of everything else.
//
// RebuildStats then describes what the pass covered rather than the collection: a
// scoped run reporting "1 artist, 4 albums" is answering about that artist.
func RebuildScoped(db *gorm.DB, scope RebuildScope) (RebuildStats, error) {
	if db == nil {
		return RebuildStats{}, nil
	}
	var stats RebuildStats
	err := retryBusy(func() error {
		stats = RebuildStats{}
		return db.Transaction(func(tx *gorm.DB) error {
			bounds, err := resolveBounds(tx, scope)
			if err != nil {
				return err
			}
			stats, err = rebuildTx(tx, bounds)
			return err
		})
	})
	if err != nil {
		return RebuildStats{}, err
	}
	return stats, nil
}

// rebuildBusyRetries is how many times a rebuild re-runs its transaction after losing
// a race with another writer. Four, spaced by a doubling-plus-jitter backoff (see
// busyBackoff) that reaches ~60ms in total — far longer than any of the writes it
// competes with, and short enough that an interactive caller does not notice.
const rebuildBusyRetries = 4

// retryBusy re-runs a transaction that SQLite refused because another writer got
// there first.
//
// It is the **second** line of defence, and no longer the first. The failure it was
// written for was Rebuild's transaction *reading before it writes*: under WAL that
// starts as a read snapshot and upgrades when it clears the disk view, and if any other
// writer commits in between, SQLite refuses the upgrade with SQLITE_BUSY_SNAPSHOT (517)
// immediately — `busy_timeout` does not apply, because a stale snapshot is not
// something waiting will fix.
//
// Retrying alone could not fix that, and it is worth being precise about why: each
// retry takes a *fresh* snapshot, which the competing writer can invalidate again, so
// against a steady stream of writes the rebuild loses indefinitely. That is a livelock,
// not bad luck, and `TestRebuildSurvivesAConcurrentWriter` duly failed on essentially
// every `-race` run. The fix is upstream of here — connections open transactions with
// `BEGIN IMMEDIATE` (`database.sqliteTxLock`), so there is no upgrade left to refuse.
//
// What remains for this function is the ordinary `SQLITE_BUSY` (5): another *process*
// holding the write lock longer than `busy_timeout`, which no locking mode prevents.
// That is rare, genuinely transient, and worth a few retries.
//
// The rebuild is safe to re-run by construction: it derives the whole disk view from
// `library_items` and the release cache, so a second attempt reads the newer state and
// produces the answer the first one was trying to. It is the losing writer that must
// retry, and every caller here is either a background pass or an interactive action
// that has already committed its real decision.
func retryBusy(run func() error) error {
	var err error
	for attempt := 0; ; attempt++ {
		if err = run(); err == nil || attempt >= rebuildBusyRetries || !isBusyError(err) {
			return err
		}
		logger.Log.Debugf("rebuild lost a write race (%s); retrying", err.Error())
		time.Sleep(busyBackoff(attempt))
	}
}

// busyBackoff spaces one retry from the next: a doubling base plus jitter of up to the
// same again. The jitter is the point — two passes that collided once will otherwise
// wake together and collide again, having waited the identical interval, which turns
// one lost race into a synchronised series of them.
func busyBackoff(attempt int) time.Duration {
	base := time.Duration(1<<attempt) * 2 * time.Millisecond
	return base + time.Duration(rand.Int64N(int64(base)))
}

// isBusyError reports whether an error is SQLite refusing a lock. Matched on the
// message rather than a driver error code so it holds across the sqlite drivers this
// app can be built with, and so a non-SQLite backend (where this cannot happen) simply
// never matches.
func isBusyError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") || strings.Contains(msg, "database table is locked")
}

// RebuildArtist re-derives one artist's disk view. It is the *Scan* verb at artist
// scope — what the artist page offers beside Process, Refresh metadata and Tag files.
func RebuildArtist(db *gorm.DB, artistMBID string) (RebuildStats, error) {
	return RebuildScoped(db, RebuildScope{ArtistMBID: artistMBID})
}

// RecordScan runs a Scan and records it as a top-level Activity event.
//
// The Scan verb was the one of the four that reported nothing at all: it answers its
// HTTP caller inline, so there was no background job to attach a row to, and pressing
// it left no trace of a pass that can move albums between artists. Being fast is not
// the same as being uninteresting.
//
// It lives here rather than in the routers so the artist and collection scopes record
// identically — they are the same verb, and two call sites is how two summaries with
// different words in them happen.
func RecordScan(db *gorm.DB, title string, scope RebuildScope, detail map[string]any) (RebuildStats, error) {
	return RecordScanUnder(db, nil, title, scope, detail)
}

// RecordScanUnder is RecordScan owned by a parent event — the same Scan, reached as a
// stage of a processing run instead of by pressing the button.
//
// A cascading activity and a hand-pressed one are the same work and must read as the
// same row; the run only changes what the row belongs to. A nil parent gives a
// top-level event, which is what the button wants.
func RecordScanUnder(db *gorm.DB, parent *models.Event, title string, scope RebuildScope, detail map[string]any) (RebuildStats, error) {
	ev := events.BeginChild(db, parent, models.EventTypeCollectionScan, title)
	stats, err := RebuildScoped(db, scope)

	status := models.EventStatusOK
	details := map[string]any{
		"artists":              stats.Artists,
		"owned_release_groups": stats.Owned,
		"credit_changes":       stats.CreditChanges,
	}
	for k, v := range detail {
		details[k] = v
	}
	summary := fmt.Sprintf("%d artists · %d albums on disk", stats.Artists, stats.Owned)
	if stats.CreditChanges > 0 {
		summary += fmt.Sprintf(" · %d credit change(s)", stats.CreditChanges)
	}
	// Appended rather than substituted: the counters are still what the pass did, and
	// a row that showed only the reason would lose the fact that it ran at all. The
	// status stays OK — nothing failed, there was simply nothing to read.
	if stats.EmptyReason != "" {
		summary += " — " + stats.EmptyReason
		details["empty_reason"] = stats.EmptyReason
	}
	if err != nil {
		status = models.EventStatusError
		details["error"] = err.Error()
		summary = "failed — " + err.Error()
	}
	ev.Stats = ScanStats(stats)
	events.Finish(db, ev, status, summary, details)
	return stats, err
}

// ScanStats is the counter set a Scan puts on its event, shared by the standalone verb
// and by the stage a run performs — the same pass, so it must not read as two.
//
// A credit change is the notable one: it is an album moving between artists, the only
// identity change with no Migrations row to click through to, so the count is the only
// way to notice one happened.
func ScanStats(stats RebuildStats) []models.EventStat {
	return []models.EventStat{
		{Label: "Artists", Value: stats.Artists},
		{Label: "Albums on disk", Value: stats.Owned},
		{Label: "Credit changes", Value: stats.CreditChanges, Kind: models.EventStatNotable},
	}
}

// bounds is a resolved RebuildScope: the rows one pass is allowed to write, held as
// sets so every narrowing step asks the same question.
//
// Two sets rather than one, because the pass clears before it re-establishes:
// `groups` bounds the release-group rows whose `owned` block this pass owns, and
// `releases` bounds the owned-edition rows it may prune. Both are resolved from the
// *current* state at the start of the pass; anything the pass then discovers is
// written and lands in `keep`, so it needs no place here.
type bounds struct {
	all      bool
	artist   string
	groups   map[string]bool
	releases map[string]bool
}

// covers reports whether a release-group's rows are this pass's to rewrite.
func (b bounds) covers(rgMBID string) bool { return b.all || b.groups[rgMBID] }

// extend admits what the pass discovered from files into its own bounds. Called
// after the clear, so it can only ever widen what gets written — never what got
// wiped. Cheap and safe on a full pass, where everything is already in scope.
func (b bounds) extend(editions []models.CollectionRelease) {
	if b.all {
		return
	}
	for _, rel := range editions {
		b.groups[rel.ReleaseGroupMBID] = true
		b.releases[rel.MBID] = true
	}
}

// keys is the sorted set as a slice, for the IN clauses. Sorted so a query plan and
// a test assertion both see a stable order; empty stays empty, which GORM renders as
// a condition that matches nothing — the right answer for a scope holding no rows.
func keys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// resolveBounds turns a scope into the sets above. A full scope resolves to "all",
// which every narrowing step short-circuits on, so the whole-collection pass runs
// exactly the queries it always did.
func resolveBounds(db *gorm.DB, scope RebuildScope) (bounds, error) {
	if scope.all() {
		return bounds{all: true}, nil
	}

	b := bounds{
		artist:   scope.ArtistMBID,
		groups:   map[string]bool{},
		releases: map[string]bool{},
	}

	groups, err := ReleaseGroupMBIDsForArtist(db, scope.ArtistMBID)
	if err != nil {
		return b, err
	}
	for _, id := range groups {
		b.groups[id] = true
	}
	// The primary-credit column is a claim in its own right — a row written before
	// the link table existed has one without a link — so it is unioned in, exactly as
	// ReleaseGroupsForArtist does.
	var owned []string
	if err := db.Model(&models.CollectionReleaseGroup{}).
		Where("artist_mb_id = ?", scope.ArtistMBID).Pluck("mb_id", &owned).Error; err != nil {
		return b, err
	}
	for _, id := range owned {
		b.groups[id] = true
	}

	releases, err := ArtistReleaseMBIDs(db, scope.ArtistMBID)
	if err != nil {
		return b, err
	}
	for _, id := range releases {
		b.releases[id] = true
	}
	return b, nil
}

// inScope reports whether one indexed file belongs to this pass.
//
// Two ways in, and the second is what keeps a scoped pass useful: an edition the
// collection already files under the artist, **or** a cached release that credits
// them. Without the second, an album whose files were only just processed — not yet
// in collection_releases — could never be discovered by the artist's own Scan, and
// the button would be unable to do the one thing it is pressed for.
func (b bounds) inScope(row itemRow) bool {
	if b.all {
		return true
	}
	if b.releases[row.MBReleaseID] {
		return true
	}
	release, ok := modules.CachedRelease(row.MBReleaseID)
	if !ok {
		return false
	}
	for _, credit := range albumArtistCredit(release) {
		if credit.Artist.ID == b.artist {
			return true
		}
	}
	return false
}

// RebuildStats is what one pass found and what it changed.
//
// Artists and Owned describe the collection as it now stands; CreditChanges describes
// what moved to get there, and is the only one of the three that is news. An upstream
// re-credit leaves no migration row — the release and the release-group both keep
// their IDs, so nothing fails and nothing is queued for review — which made an album
// silently changing artists the one identity change with no trace anywhere. It is
// reported per run rather than stored, because the state it describes is already
// correct by the time anyone reads it; what is missing is that it happened.
type RebuildStats struct {
	Artists int `json:"artists"`
	Owned   int `json:"owned"`
	// CreditChanges is release-groups whose primary credit moved plus credit links
	// dropped because MusicBrainz no longer names the artist.
	CreditChanges int `json:"credit_changes"`
	// EmptyReason names the input that was missing when a pass found nothing, and is
	// blank whenever the pass had something to work from — including when it honestly
	// found zero.
	//
	// Scan re-derives the collection from `library_items`, so on a cold install it
	// answers "0 artists, 0 albums" and looks like a verb that does not work. It is
	// telling the truth; what it cannot say is that Process has never run. The four
	// verbs feed each other (Process → `library_items` → Scan → collection rows →
	// Sync → catalog columns), so on a fresh install every verb but the first reports
	// an honest zero that reads as a dud.
	EmptyReason string `json:"empty_reason,omitempty"`
}

// The reasons a Scan can have nothing to re-derive. Exported so a caller can render
// them and a test can assert one without restating the sentence.
const (
	// ScanEmptyNoFiles is a cold install: nothing has ever walked a library.
	ScanEmptyNoFiles = "no files are indexed yet — run Process to walk your libraries"
	// ScanEmptyNothingMatched is the second state: files are indexed, but none of them
	// has been resolved to a MusicBrainz release, so there is nothing to own. The
	// distinction matters because the fix is different — this one is a manager or
	// MusicBrainz problem, not a "you have not started yet" problem.
	ScanEmptyNothingMatched = "files are indexed but none is matched to a release yet — check Items for what failed"
	// ScanEmptyArtistNoFiles is the artist-scoped version of the same thing.
	ScanEmptyArtistNoFiles = "no matched files for this artist"
)

// scanEmptyReason works out why a pass re-derived nothing. It runs only when the pass
// found neither an artist nor an owned album, so the extra count it costs is paid on
// the empty install and never on a working one.
//
// `all` is every matched row in the index, `scoped` the subset this pass covered — the
// two differ only for an artist-scoped pass, which is exactly when "the collection has
// files, this artist does not" is the useful answer.
func scanEmptyReason(db *gorm.DB, scope bounds, all, scoped int) string {
	if scoped > 0 {
		// The pass had input and still found nothing. That is a real answer about the
		// data, not a missing precondition, and inventing a reason for it would be
		// worse than the bare zero.
		return ""
	}
	if all > 0 {
		if !scope.all {
			return ScanEmptyArtistNoFiles
		}
		// Unreachable in practice — an unscoped pass covers every matched row — but
		// falling through to "nothing is matched" here would be a lie.
		return ""
	}

	var indexed int64
	if err := db.Model(&models.LibraryItem{}).Count(&indexed).Error; err != nil {
		logger.Log.Warnf("failed to count indexed files while explaining an empty scan: %s", err.Error())
		return ""
	}
	if indexed == 0 {
		return ScanEmptyNoFiles
	}
	if scope.all {
		return ScanEmptyNothingMatched
	}
	return ScanEmptyArtistNoFiles
}

// rebuildTx is the body of Rebuild, run inside the caller's transaction. Every write
// below goes through the handle passed in, so a failure at any point rolls the whole
// re-derivation back rather than leaving the disk view half-cleared.
func rebuildTx(db *gorm.DB, scope bounds) (RebuildStats, error) {
	// library -> manager type (for the per-artist "managed by" provenance)
	libraryManager, err := libraryManagerTypes(db)
	if err != nil {
		return RebuildStats{}, err
	}

	// Clear the disk view; it is re-established below. Track counts must be cleared
	// too, or a row that stops being owned keeps stale counts and renders as
	// "missing, 10/12". The catalog columns are untouched — they belong to the
	// manager mirror, which is the whole point of keeping them separate.
	//
	// A scoped pass clears only the release-groups it covers. This is the line the
	// whole scope exists for: clearing wholesale and re-establishing from one
	// artist's files would report the rest of the collection as owning nothing.
	clear := db.Model(&models.CollectionReleaseGroup{}).Where("owned = ?", true)
	if !scope.all {
		clear = clear.Where("mb_id IN ?", keys(scope.groups))
	}
	if err := clear.Updates(map[string]any{"owned": false, "owned_tracks": 0, "total_tracks": 0}).Error; err != nil {
		return RebuildStats{}, err
	}

	all, err := ownedItemRows(db)
	if err != nil {
		return RebuildStats{}, err
	}
	rows := make([]itemRow, 0, len(all))
	for _, r := range all {
		if scope.inScope(r) {
			rows = append(rows, r)
		}
	}

	// Pass 1: count owned files per release, and gather artist + manager provenance.
	releaseOwned := map[string]int{}
	artistName := map[string]string{}
	artistManagers := map[string]map[string]bool{}
	for _, r := range rows {
		releaseOwned[r.MBReleaseID]++

		release, ok := modules.CachedRelease(r.MBReleaseID)
		if !ok {
			continue
		}
		albumCredit := albumArtistCredit(release)
		if len(albumCredit) == 0 {
			continue
		}
		mt := libraryManager[r.LibraryID]
		if mt == "" {
			// The item points at a library that no longer exists.
			mt = models.ManagedByUnknown
		}
		// Every credited artist, not just the first: on a collaboration the second
		// artist owns these files just as much, and crediting only the first left them
		// with no collection artist row and no provenance at all.
		for _, credit := range albumCredit {
			artistID := credit.Artist.ID
			if artistID == "" {
				continue
			}
			artistName[artistID] = credit.Artist.Name
			if artistManagers[artistID] == nil {
				artistManagers[artistID] = map[string]bool{}
			}
			artistManagers[artistID][mt] = true
		}
	}

	// Pass 2: per release-group, keep the best-owned edition and its track counts —
	// and, separately, every owned edition. The release-group summary stays
	// best-edition (that is the useful headline: "how close am I to having this
	// album"), while the per-edition rows keep the detail that summary hides.
	type rgInfo struct {
		artistID, title, primary, secondary, date string
		// credits is the album's full artist credit, in order. Rebuild is the only
		// writer that reads the release itself, so it is the only one that can record
		// who else is on a collaboration.
		credits      []string
		owned, total int
	}
	rgBest := map[string]rgInfo{}
	ownedEditions := make([]models.CollectionRelease, 0, len(releaseOwned))
	for relID, ownedTracks := range releaseOwned {
		release, ok := modules.CachedRelease(relID)
		if !ok {
			continue
		}
		rgID := release.ReleaseGroup.ID
		albumCredit := albumArtistCredit(release)
		if rgID == "" || len(albumCredit) == 0 {
			continue
		}
		total := 0
		for _, m := range release.Media {
			total += len(m.Tracks)
		}
		credits := make([]string, 0, len(albumCredit))
		for _, credit := range albumCredit {
			if credit.Artist.ID != "" {
				credits = append(credits, credit.Artist.ID)
			}
		}
		if len(credits) == 0 {
			continue
		}
		artistID := credits[0]

		ownedEditions = append(ownedEditions, models.CollectionRelease{
			MBID: relID, ReleaseGroupMBID: rgID, ArtistMBID: artistID,
			Title: release.Title, Date: release.Date, Country: release.Country,
			Disambiguation: release.Disambiguation, Format: mediaSummary(release),
			OwnedTracks: ownedTracks, TotalTracks: total,
		})

		if cur, exists := rgBest[rgID]; !exists || ownedTracks > cur.owned {
			rgBest[rgID] = rgInfo{
				artistID:  artistID,
				credits:   credits,
				title:     release.ReleaseGroup.Title,
				primary:   release.ReleaseGroup.PrimaryType,
				secondary: strings.Join(release.ReleaseGroup.SecondaryTypes, ", "),
				date:      release.ReleaseGroup.FirstReleaseDate,
				owned:     ownedTracks,
				total:     total,
			}
		}
	}
	// An album the pass just discovered — files processed since the last rebuild, so
	// no collection row named it yet — becomes this pass's to write from here on. The
	// clear above is already done, so admitting it late affects only what follows:
	// its editions survive the prune (they are owned) and its wants are reconciled
	// rather than skipped as out of bounds.
	scope.extend(ownedEditions)

	if err := syncOwnedReleases(db, ownedEditions, scope); err != nil {
		return RebuildStats{}, err
	}

	for artistID, mgrs := range artistManagers {
		if err := upsertArtist(db, artistID, artistName[artistID], managedByLabel(mgrs)); err != nil {
			return RebuildStats{}, err
		}
	}
	// Rebuild is the only writer that reports credit movement, because it is the only
	// one reading MusicBrainz's own answer about whose album this is.
	changes := &creditChanges{}
	for rgID, info := range rgBest {
		if err := upsertReleaseGroup(db, rgWrite{
			mbID: rgID, artistMBID: info.artistID, credits: info.credits, title: info.title,
			primary: info.primary, secondary: info.secondary, date: info.date,
			disk:    &diskState{owned: true, ownedTracks: info.owned, totalTracks: info.total},
			changes: changes,
		}); err != nil {
			return RebuildStats{}, err
		}
	}

	// The upserts above re-derived the credit graph, including unlinking artists
	// MusicBrainz has moved a release-group off. That can leave an artist holding
	// nothing at all, so this runs after them and never before.
	if pruned, err := pruneOrphanArtists(db); err != nil {
		return RebuildStats{}, err
	} else if pruned > 0 {
		logger.Log.Infof("removed %d collection artist(s) nothing is credited to any more", pruned)
	}

	// Runs last: it reads the artists' managed_by (upserted just above) and the owned
	// editions to keep auto-selected edition wants tracking the files.
	if err := reconcileAutoDesires(db, ownedEditions, scope); err != nil {
		return RebuildStats{}, err
	}

	stats := RebuildStats{Artists: len(artistManagers), Owned: len(rgBest), CreditChanges: changes.total()}
	if stats.Artists == 0 && stats.Owned == 0 {
		stats.EmptyReason = scanEmptyReason(db, scope, len(all), len(rows))
	}
	return stats, nil
}

// albumArtistCredit returns whose *album* a cached release belongs to: the
// release-group's own MusicBrainz credit, falling back to the release's when the
// group carries none.
//
// The two are credited independently upstream and routinely disagree. An artist
// migration is applied to the release-group and to the pressings an editor gets
// round to, so older editions keep the previous credit indefinitely — a soundtrack
// moved from "Various Artists" to its composers still has Various Artists on its
// original CD. Crediting ownership from the release therefore filed the album under
// an artist the release-group itself no longer named, and no amount of re-scanning
// fixed it, because the release was being read correctly.
//
// The fallback is what keeps this safe rather than merely different: a group with no
// credit of its own (an older cache entry, or a payload that omits it) behaves
// exactly as before instead of dropping the album out of the collection.
//
// The release's own credit is still the right answer for a *pressing* — it is simply
// not the answer to "whose album is this", which is the only question Rebuild asks.
func albumArtistCredit(release models.MusicBrainzReleaseResponse) []models.ArtistCredit {
	if len(release.ReleaseGroup.ArtistCredit) > 0 {
		return release.ReleaseGroup.ArtistCredit
	}
	return release.ArtistCredit
}

// reconcileAutoDesires keeps auto-selected edition wants tracking the files (follow-up
// C). When the user wants "any" edition of an album and files land, the want migrates to
// the owned edition as an auto desire; if the files are later replaced with a different
// edition it re-points, and if they are removed it is pruned. Only native artists are
// managed — under Lidarr the edition comes from the monitored release, not a desire — and
// only albums the user actually asked for (an "any" want, or a prior auto want) are
// touched, so owning a file never manufactures a want on its own. A hand-pinned edition
// (source = manual) is never removed or re-pointed here; the provenance is what tells
// them apart. Manager-derived rows belong to reconcileManagerDesires and are likewise
// left alone — they only exist for artists this pass already skips.
func reconcileAutoDesires(db *gorm.DB, ownedEditions []models.CollectionRelease, scope bounds) error {
	ownedByRG := map[string]map[string]bool{}
	for _, rel := range ownedEditions {
		if ownedByRG[rel.ReleaseGroupMBID] == nil {
			ownedByRG[rel.ReleaseGroupMBID] = map[string]bool{}
		}
		ownedByRG[rel.ReleaseGroupMBID][rel.MBID] = true
	}

	var stored []models.CollectionDesire
	if err := db.Find(&stored).Error; err != nil {
		return err
	}
	// A scoped pass only knows which editions are owned inside its own bounds, so a
	// want outside them would be read as "nothing owns this" and pruned. Filtering
	// here rather than in the query keeps one code path: the full pass sees every row,
	// as before.
	desires := make([]models.CollectionDesire, 0, len(stored))
	for _, d := range stored {
		if scope.covers(d.ReleaseGroupMBID) {
			desires = append(desires, d)
		}
	}
	if len(desires) == 0 {
		return nil
	}

	// managed_by per artist, so the native-only rule costs no query per desire.
	editable := map[string]bool{}
	var artists []models.CollectionArtist
	if err := db.Find(&artists).Error; err != nil {
		return err
	}
	for _, a := range artists {
		editable[a.MBID] = IdentityEditable(a)
	}

	// A release-group is under auto management if the user expressed intent for it (an
	// "any" want) or we already promoted it (an auto want). Evaluated on the current
	// rows, before any are pruned below, so a re-point — delete the old auto, add the
	// new one — does not lose the trigger midway.
	autoManaged := map[string]bool{}
	for _, d := range desires {
		if editable[d.ArtistMBID] && (d.ReleaseMBID == "" || d.Source == models.DesireSourceAuto) {
			autoManaged[d.ReleaseGroupMBID] = true
		}
	}

	// Prune stale auto wants: an auto edition no longer owned (files moved off it, or
	// were removed) has stopped representing anything.
	for _, d := range desires {
		if d.Source == models.DesireSourceAuto && !ownedByRG[d.ReleaseGroupMBID][d.ReleaseMBID] {
			if err := db.Delete(&models.CollectionDesire{}, "id = ?", d.ID).Error; err != nil {
				return err
			}
		}
	}

	// Promote / re-point: for each auto-managed group that owns something, drop the
	// "any" want and ensure one auto want per owned edition that has no desire yet.
	for rgMBID := range autoManaged {
		owned := ownedByRG[rgMBID]
		if len(owned) == 0 {
			continue // still wanted, nothing owned: leave the "any" want untouched
		}

		artistMBID := ""
		haveEdition := map[string]bool{} // editions already desired (manual or surviving auto)
		var anyIDs []uuid.UUID
		for _, d := range desires {
			if d.ReleaseGroupMBID != rgMBID {
				continue
			}
			if artistMBID == "" {
				artistMBID = d.ArtistMBID
			}
			if d.ReleaseMBID == "" {
				anyIDs = append(anyIDs, d.ID)
			} else {
				haveEdition[d.ReleaseMBID] = true
			}
		}
		if len(anyIDs) > 0 {
			if err := db.Delete(&models.CollectionDesire{}, "id IN ?", anyIDs).Error; err != nil {
				return err
			}
		}
		for relID := range owned {
			if haveEdition[relID] {
				continue // a manual or already-owned auto want covers this edition
			}
			if err := db.Create(&models.CollectionDesire{
				ArtistMBID: artistMBID, ReleaseGroupMBID: rgMBID, ReleaseMBID: relID,
				Source: models.DesireSourceAuto,
			}).Error; err != nil {
				return err
			}
		}
	}

	return nil
}

// syncOwnedReleases makes the per-edition disk table match exactly the editions
// currently owned: upsert what is owned, delete what is not.
//
// Pruning matters more here than upserting. A file re-attached from the original to
// the remaster (which manual attach now makes easy) leaves the original owning
// nothing, and a stale row would keep claiming files that moved — the same class of
// bug as the release-group counts that used to survive losing their files.
// syncOwnedReleases returns an error if any write failed. Rebuild treats that as
// fatal and rolls back: a re-derivation that half-landed leaves the collection
// claiming to own less than it does, and a silently wrong view is worse than an
// error the user can retry. Per-row failures are still logged individually, because
// which row failed is the useful diagnostic.
func syncOwnedReleases(db *gorm.DB, owned []models.CollectionRelease, scope bounds) error {
	var failed error
	keep := make([]string, 0, len(owned))
	for _, rel := range owned {
		keep = append(keep, rel.MBID)

		var existing models.CollectionRelease
		if err := db.Where("mb_id = ?", rel.MBID).First(&existing).Error; err == nil {
			if err := db.Model(&existing).Updates(map[string]any{
				"release_group_mb_id": rel.ReleaseGroupMBID,
				"artist_mb_id":        rel.ArtistMBID,
				"title":               rel.Title,
				"date":                rel.Date,
				"country":             rel.Country,
				"disambiguation":      rel.Disambiguation,
				"format":              rel.Format,
				"owned_tracks":        rel.OwnedTracks,
				"total_tracks":        rel.TotalTracks,
			}).Error; err != nil {
				logger.Log.Warnf("failed to update owned edition %s: %s", rel.MBID, err.Error())
				failed = err
			}
			continue
		}
		if err := db.Create(&rel).Error; err != nil {
			logger.Log.Warnf("failed to record owned edition %s: %s", rel.MBID, err.Error())
			failed = err
		}
	}

	// Owning nothing is a real state, and it must still clear the table — a
	// "NOT IN ()" with no values would delete nothing at all.
	//
	// A scoped pass may only delete inside its bounds, and for the same reason the
	// clear is bounded: every edition of every other artist is "not owned" as far as
	// this pass can see, and an unbounded NOT IN would take the lot.
	prune := db.Session(&gorm.Session{AllowGlobalUpdate: true})
	if !scope.all {
		prune = prune.Where("mb_id IN ?", keys(scope.releases))
	}
	if len(keep) > 0 {
		prune = prune.Where("mb_id NOT IN ?", keep)
	}
	if err := prune.Delete(&models.CollectionRelease{}).Error; err != nil {
		logger.Log.Warnf("failed to prune owned editions: %s", err.Error())
		failed = err
	}
	return failed
}

// mediaSummary describes a release's media the way an edition list needs it:
// "2×CD", "CD + DVD", "Digital Media". It is what distinguishes one edition from
// another at a glance, alongside the year.
func mediaSummary(release models.MusicBrainzReleaseResponse) string {
	if len(release.Media) == 0 {
		return ""
	}
	// Consecutive identical formats collapse to a count; a mixed set is listed.
	var parts []string
	count := 0
	current := ""
	flush := func() {
		if current == "" && count == 0 {
			return
		}
		name := current
		if name == "" {
			name = "Unknown"
		}
		if count > 1 {
			name = fmt.Sprintf("%d×%s", count, name)
		}
		parts = append(parts, name)
	}
	for _, m := range release.Media {
		if m.Format != current {
			flush()
			current, count = m.Format, 0
		}
		count++
	}
	flush()
	return strings.Join(parts, " + ")
}

// SyncArtist fetches an artist's discography and records the release-groups that
// following the artist wants but does not own. Owned rows keep their owned flag.
// The artist's own follow settings decide which types count.
func SyncArtist(db *gorm.DB, meta metadata.MetadataSource, artistMBID string) (wanted int, err error) {
	var artist models.CollectionArtist
	if err := db.Where("mb_id = ?", artistMBID).First(&artist).Error; err != nil {
		return 0, err
	}

	// Read the artist through the cache, so one that was just added is in the mirror
	// before anyone opens their page.
	//
	// This used to drop the cached copy first and re-read over the network, to catch
	// an artist merged upstream. The justification was that nothing else re-read an
	// artist on a schedule; that stopped being true when the refresh pass began
	// covering every artist in the collection on its TTL, and redirects are recorded
	// on the HTTP path by whatever fetch sees them. What was left was a follow toggle
	// silently spending a rate-limited request and discarding the stale fallback with
	// it — a cache reset nothing in the UI announced.
	if _, err := meta.GetArtist(artistMBID); err != nil {
		logger.Log.Debugf("could not read artist %s: %s", artistMBID, err.Error())
	}

	groups, complete, err := meta.GetArtistReleaseGroups(artistMBID)
	if err != nil {
		return 0, err
	}
	// Pruning uses the *unfiltered* discography, deliberately: `groups` is everything
	// MusicBrainz has, while the loop below only upserts the types this artist's
	// follow settings want. Comparing stored rows against the filtered set would
	// delete every release-group of a type the user does not follow.
	if complete {
		if pruned, err := PruneOrphanReleaseGroups(db, artistMBID, groups); err != nil {
			logger.Log.Warnf("failed to prune orphaned release-groups for %s: %s", artistMBID, err.Error())
		} else if pruned > 0 {
			logger.Log.Infof("pruned %d release-group(s) MusicBrainz no longer lists for %s", pruned, artist.Name)
		}
	}

	for _, rg := range groups {
		if !FollowWants(artist, rg.PrimaryType, rg.SecondaryTypes, rg.FirstReleaseDate) {
			continue
		}
		// The MusicBrainz discography is the native manager's catalog. Track counts
		// stay unknown (0) — counting them would mean fetching every release.
		_ = upsertReleaseGroup(db, rgWrite{
			mbID: rg.ID, artistMBID: artistMBID, title: rg.Title,
			primary: rg.PrimaryType, secondary: strings.Join(rg.SecondaryTypes, ", "),
			date:    rg.FirstReleaseDate,
			catalog: &catalogState{},
		})
		wanted++
	}
	now := time.Now()
	if err := db.Model(&models.CollectionArtist{}).Where("mb_id = ?", artistMBID).Update("last_synced_at", now).Error; err != nil {
		logger.Log.Warnf("failed to set last_synced_at for artist %s: %s", artistMBID, err.Error())
	}
	return wanted, nil
}

// FollowWants reports whether following this artist auto-wants a release-group of
// the given types, released on or after the artist's cutoff. It is the single
// definition of that rule — the discography sync, the API's "why is this wanted"
// label, and the UI all go through it, so changing an artist's follow settings
// changes every view at once.
//
// Unset FollowTypes means the default (studio albums + EPs), which is what keeps a
// missing list readable; a discography is mostly singles and reissues.
//
// `date` is the release-group's MusicBrainz first-release date in whatever precision
// it has (`YYYY`, `YYYY-MM`, `YYYY-MM-DD`, or empty).
func FollowWants(artist models.CollectionArtist, primaryType string, secondaryTypes []string, date string) bool {
	if !artist.FollowSecondary && len(secondaryTypes) > 0 {
		return false
	}
	if !followWantsYear(artist.FollowFromYear, date) {
		return false
	}

	wanted := strings.TrimSpace(artist.FollowTypes)
	if wanted == "" {
		wanted = models.DefaultFollowTypes
	}
	primary := strings.ToLower(strings.TrimSpace(primaryType))
	for _, t := range strings.Split(wanted, ",") {
		if strings.ToLower(strings.TrimSpace(t)) == primary {
			return true
		}
	}
	return false
}

// followWantsYear applies the release-year cutoff. No cutoff (0) wants everything,
// which is what following meant before the field existed and is still the default.
//
// **An undated release-group is excluded once a cutoff is set.** That is the choice
// worth stating, because the other reading is defensible too. A cutoff is opt-in and
// its entire purpose is to keep the back catalogue out of the missing list; a
// release-group MusicBrainz has no date for is far more likely to be an obscure old
// entry than a new record, since anything actually being released is dated upstream
// before it comes out. Including them would let the noise the cutoff exists to remove
// back in through the one gap nobody can see.
func followWantsYear(cutoff int, date string) bool {
	if cutoff <= 0 {
		return true
	}
	year, ok := releaseYear(date)
	if !ok {
		return false
	}
	return year >= cutoff
}

// releaseYear reads the year off a MusicBrainz date. Every precision MusicBrainz
// stores — `YYYY`, `YYYY-MM`, `YYYY-MM-DD` — starts with the four-digit year, so the
// prefix is the whole of what this needs and a partial date costs nothing.
func releaseYear(date string) (int, bool) {
	date = strings.TrimSpace(date)
	if len(date) < 4 {
		return 0, false
	}
	year, err := strconv.Atoi(date[:4])
	if err != nil || year <= 0 {
		return 0, false
	}
	return year, true
}

// FollowGoverns reports whether the *native* follow settings decide what is wanted
// for this artist. They do not when a manager owns the artist: Lidarr is the
// authority on wanted there, and the artist page says so.
//
// Without this, a stale Monitored flag — set before the artist became
// Lidarr-managed, or by the old artist-level "Monitored" toggle — kept producing
// automatic wants on a page that offers no follow control to turn them off. The
// state was real, but nothing on screen could explain or change it. Following is
// still recorded for such an artist; it just does not govern until the artist is
// natively managed again.
func FollowGoverns(artist models.CollectionArtist) bool {
	return artist.ManagedBy != models.ManagedByLidarr && artist.ManagedBy != models.ManagedByMixed
}

// CatalogChecked reports whether a manager has actually been asked what this artist's
// catalogue contains — the precondition for comparing the disk view against it.
//
// It is the single definition of "there is a catalog to disagree with", and it reads
// LastSyncedAt rather than the release-group rows because those answer a different
// question. Deriving it from "does any of this artist's albums carry catalog state"
// meant an album could be reported as absent from the manager on the strength of
// *other* albums having been mirrored — so a release-group filed under the wrong
// artist warned "not in Lidarr" about an album Lidarr had, under a different artist,
// that nothing had ever put to Lidarr. Absence of an answer is not a negative answer.
//
// The false case is deliberately quiet: an artist no manager has synced gets no
// discrepancy on any album, which is right for a native artist nobody follows and is
// also what an unsynced Lidarr artist should show until the mirror has run once.
// Both SyncArtist and SyncLidarr stamp LastSyncedAt, so this is per-manager-agnostic.
func CatalogChecked(artist models.CollectionArtist) bool {
	return artist.LastSyncedAt != nil
}

// IdentityEditable reports whether the user may set a MusicBrainz identity for this
// artist's files by hand — attaching an unmatched file, wanting a specific edition,
// choosing a release. It is the identity-side sibling of FollowGoverns: false when a
// manager owns identity. Under the rule "if Lidarr governs the artist at all, identity
// is Lidarr's", that means the Lidarr and mixed cases. For those artists the release,
// the edition and the track are Lidarr's to decide, and Autotaggerr only tags to match;
// a hand-attach there would be reverted by the next scan anyway.
func IdentityEditable(artist models.CollectionArtist) bool {
	return artist.ManagedBy != models.ManagedByLidarr && artist.ManagedBy != models.ManagedByMixed
}

// ArtistIdentityEditable resolves IdentityEditable for an artist by MB ID. An artist
// with no collection row yet is editable: nothing owns it, so the native path (manual
// choice) is the only authority there is. A lookup error other than "not found" is
// returned so the caller can fail closed rather than silently allowing an edit.
func ArtistIdentityEditable(db *gorm.DB, artistMBID string) (bool, error) {
	var artist models.CollectionArtist
	err := db.Where("mb_id = ?", strings.TrimSpace(artistMBID)).First(&artist).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return IdentityEditable(artist), nil
}

// FollowWantsStored is FollowWants for a stored CollectionReleaseGroup row, whose
// secondary types are comma-joined rather than a slice and whose date is the column
// the discography sync wrote.
func FollowWantsStored(artist models.CollectionArtist, primary, secondaryCSV, date string) bool {
	return FollowWants(artist, primary, splitTypes(secondaryCSV), date)
}

func splitTypes(csv string) []string {
	var out []string
	for _, part := range strings.Split(csv, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// libraryManagerTypes maps every library to the kind of manager that owns it, which
// is where an artist's provenance ultimately comes from: an artist is managed by
// whatever manages the libraries their files sit in.
//
// Shared by Rebuild (which derives provenance for every artist at once) and
// deriveArtistManager (which does it for one), so the three-way answer below —
// native, the manager's type, or unknown — has a single definition.
func libraryManagerTypes(db *gorm.DB) (map[uuid.UUID]string, error) {
	managerType := map[uuid.UUID]string{}
	var managers []models.Manager
	if err := db.Find(&managers).Error; err != nil {
		return nil, err
	}
	for _, m := range managers {
		managerType[m.ID] = m.Type
	}

	libraryManager := map[uuid.UUID]string{}
	var libraries []models.Library
	if err := db.Find(&libraries).Error; err != nil {
		return nil, err
	}
	for _, l := range libraries {
		switch {
		case l.ManagerID == nil:
			// No manager assigned is the documented native default, not unknown.
			libraryManager[l.ID] = models.ManagerTypeAutotaggerr
		case managerType[*l.ManagerID] != "":
			libraryManager[l.ID] = managerType[*l.ManagerID]
		default:
			// Dangling reference: the manager row is gone. Say so rather than
			// letting it fall through to "native".
			libraryManager[l.ID] = models.ManagedByUnknown
		}
	}
	return libraryManager, nil
}

// managedByLabel summarises which managers own an artist's files. Unknown is a
// real answer, not a fallback: an artist whose provenance cannot be determined must
// not be reported as natively managed, because that is a claim rather than an
// absence of one.
func managedByLabel(mgrs map[string]bool) string {
	lidarr := mgrs[models.ManagerTypeLidarr]
	native := mgrs[models.ManagerTypeAutotaggerr]
	switch {
	case lidarr && native:
		return models.ManagedByMixed
	case lidarr:
		return models.ManagedByLidarr
	case native:
		return models.ManagedByAutotaggerr
	default:
		return models.ManagedByUnknown
	}
}

// upsertArtist returns an error so Rebuild can roll back on a failed write. The
// sync paths log it and carry on instead, which is why the error is returned rather
// than acted on here.
func upsertArtist(db *gorm.DB, mbID, name, managedBy string) error {
	var a models.CollectionArtist
	if err := db.Where("mb_id = ?", mbID).First(&a).Error; err == nil {
		// A detached artist keeps its native provenance whatever manages the library
		// its files are in. Without this the re-derivation below would hand the artist
		// straight back to the manager on the next scan, so the detach would appear to
		// work and then quietly undo itself — see models.CollectionArtist.
		if a.ManagerDetached {
			managedBy = models.ManagedByAutotaggerr
		}
		// Preserve Monitored / LastSyncedAt / Origin; refresh name + provenance.
		// Origin records how the artist *entered* the collection, so an artist added
		// by hand keeps that origin once files for it show up.
		if err := db.Model(&a).Updates(map[string]any{"name": name, "managed_by": managedBy}).Error; err != nil {
			logger.Log.Warnf("failed to update artist %s: %s", mbID, err.Error())
			return err
		}
		return nil
	}
	if err := db.Create(&models.CollectionArtist{
		MBID: mbID, Name: name, ManagedBy: managedBy,
		Origin: models.CollectionOriginLibrary,
	}).Error; err != nil {
		logger.Log.Warnf("failed to upsert artist %s: %s", mbID, err.Error())
		return err
	}
	return nil
}

// diskState is what Rebuild observed on disk.
type diskState struct {
	owned                    bool
	ownedTracks, totalTracks int
}

// catalogState is what a manager reports should exist. Zero totalTracks means the
// manager did not say (native MB discovery), and an empty releaseMBID that it
// selected no edition.
type catalogState struct {
	ownedTracks, totalTracks int
	monitored                bool
	releaseMBID              string
}

// rgWrite is one caller's knowledge of a release-group. Metadata is always written;
// the disk and catalog blocks are written only when the caller owns that view, so
// Rebuild and the manager mirror can run in any order without clobbering each other.
type rgWrite struct {
	mbID, artistMBID, title, primary, secondary, date string
	// credits is every credited artist in MusicBrainz credit order. Only a caller
	// that read the actual release knows it (Rebuild); everyone else leaves it empty,
	// which means "artistMBID is credited" and must not be read as "artistMBID is the
	// only artist" — mistaking the two is what made collaborations flip between
	// artists. See collection/credits.go.
	credits []string
	disk    *diskState
	catalog *catalogState
	// changes, when non-nil, accumulates what this write altered about the credit
	// graph. Only Rebuild sets it: a mirror rewriting its own catalog is not an
	// upstream re-credit, and counting it would bury the signal in noise.
	changes *creditChanges
}

// upsertReleaseGroup returns an error so Rebuild can roll back on a failed write.
// The sync paths ignore it and carry on, which is the resilience they have always
// had — one unwritable album must not abandon a whole discography sync.
func upsertReleaseGroup(db *gorm.DB, w rgWrite) error {
	updates := map[string]any{
		"title": w.title, "primary_type": w.primary,
		"secondary_types": w.secondary, "first_release_date": w.date,
	}
	// The primary credit is only rewritten by a caller that knows the real credit
	// order. A discography sync knows the artist it is syncing is credited, not that
	// they are credited *first*, so letting it write this column made the second
	// artist of a collaboration claim an album that is not theirs to head.
	if len(w.credits) > 0 {
		updates["artist_mb_id"] = w.credits[0]
	}
	if w.disk != nil {
		updates["owned"] = w.disk.owned
		updates["owned_tracks"] = w.disk.ownedTracks
		updates["total_tracks"] = w.disk.totalTracks
	}
	if w.catalog != nil {
		updates["in_catalog"] = true
		updates["catalog_owned_tracks"] = w.catalog.ownedTracks
		updates["catalog_total_tracks"] = w.catalog.totalTracks
		updates["catalog_monitored"] = w.catalog.monitored
		updates["catalog_release_mb_id"] = w.catalog.releaseMBID
	}

	// Whoever this write is about is credited, whether or not the caller knows the
	// full credit. Links are additive, so a partial writer adds its artist without
	// removing anyone else's claim.
	credited := w.credits
	if len(credited) == 0 && w.artistMBID != "" {
		credited = []string{w.artistMBID}
	}
	// Only the disk writer reads a release-group's real credit, which is what makes it
	// the only caller allowed to take a link away — and only for the group it just
	// re-derived. Linking runs first so the prune sees this pass's own claims.
	source := creditFromCatalog
	if w.disk != nil {
		source = creditFromDisk
	}
	defer func() {
		linkReleaseGroupArtists(db, w.mbID, credited, len(w.credits) > 0, source)
		if source == creditFromDisk && len(w.credits) > 0 {
			w.changes.unlink(pruneReleaseGroupArtists(db, w.mbID, w.credits))
		}
	}()

	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", w.mbID).First(&rg).Error; err == nil {
		// A primary credit moving between two named artists is an upstream re-credit.
		// Filling in a blank one is not: that is this writer learning something, not
		// MusicBrainz changing its mind.
		if primary, ok := updates["artist_mb_id"].(string); ok && rg.ArtistMBID != "" && rg.ArtistMBID != primary {
			logger.Log.Infof("release-group %s (%s) moved from artist %s to %s upstream", w.mbID, w.title, rg.ArtistMBID, primary)
			w.changes.regroup()
		}
		if err := db.Model(&rg).Updates(updates).Error; err != nil {
			logger.Log.Warnf("failed to update release-group %s: %s", w.mbID, err.Error())
			return err
		}
		return nil
	}

	// On create there is nothing to preserve, so the caller's artist is the best
	// available primary even when the full credit is unknown.
	primaryArtist := w.artistMBID
	if len(w.credits) > 0 {
		primaryArtist = w.credits[0]
	}
	row := models.CollectionReleaseGroup{
		MBID: w.mbID, ArtistMBID: primaryArtist, Title: w.title, PrimaryType: w.primary,
		SecondaryTypes: w.secondary, FirstReleaseDate: w.date,
	}
	if w.disk != nil {
		row.Owned, row.OwnedTracks, row.TotalTracks = w.disk.owned, w.disk.ownedTracks, w.disk.totalTracks
	}
	if w.catalog != nil {
		row.InCatalog = true
		row.CatalogOwnedTracks, row.CatalogTotalTracks = w.catalog.ownedTracks, w.catalog.totalTracks
		row.CatalogMonitored = w.catalog.monitored
		row.CatalogReleaseMBID = w.catalog.releaseMBID
	}
	if err := db.Create(&row).Error; err != nil {
		logger.Log.Warnf("failed to upsert release-group %s: %s", w.mbID, err.Error())
		return err
	}
	return nil
}

// SyncOptions narrows and tunes a Lidarr mirror pass.
type SyncOptions struct {
	// ArtistMBID limits the pass to one artist. Empty syncs every Lidarr-managed
	// artist in the collection, which is what the collection-wide button asks for.
	ArtistMBID string
	// IgnoreCache drops the cached Lidarr responses for each artist synced, so the
	// *next scan* re-asks Lidarr instead of matching against data up to an hour old.
	//
	// It does nothing for the sync's own numbers: the two calls this pass makes
	// (GetArtists, GetArtistAlbums) are the only uncached Lidarr calls there are, so a
	// mirror pass is always fresh. What goes stale is the artist's track file list,
	// which is what the pipeline matches paths against — a file imported into Lidarr
	// after that list was cached cannot be matched until it expires. Hence an option
	// rather than the default: it is repair, not part of mirroring.
	IgnoreCache bool
}

// SyncStats is what one mirror pass did, and — when it did nothing — why.
//
// EmptyReason is the whole reason this is a struct rather than two ints. A pass that
// returns before its first HTTP call and one that asked Lidarr and got nothing both
// reported "0 artists synced · 0 albums", so "there was nothing here to mirror" and
// "Lidarr has nothing for these artists" were the same Activity row. They need
// different things done about them.
type SyncStats struct {
	ArtistsSynced int    `json:"artists_synced"`
	Groups        int    `json:"albums"`
	EmptyReason   string `json:"empty_reason,omitempty"`
}

// The reasons a Lidarr mirror pass can have nothing to mirror. Each one names a
// different missing precondition, and each has a different fix.
const (
	// SyncEmptyNoManager is no enabled Lidarr manager at all. The collection-wide
	// button is not offered in this state and the API rejects the call with a 400, so
	// this is reached by the scheduled run.
	SyncEmptyNoManager = "no enabled Lidarr manager is configured"
	// SyncEmptyNoManagerCredentials is a manager row that exists but cannot be called.
	SyncEmptyNoManagerCredentials = "the Lidarr manager has no URL or API key"
	// SyncEmptyNoArtists is a collection with nothing in it — the cold install, where
	// Process has not run and no artist has been added by hand.
	SyncEmptyNoArtists = "the collection has no artists yet — run Process, or add an artist"
	// SyncEmptyNoneManaged is the interesting one: there are artists, but none of them
	// is Lidarr's. Mirroring cannot introduce an artist, so this pass has nothing to
	// ask about however healthy Lidarr is.
	SyncEmptyNoneManaged = "no artist in the collection is managed by Lidarr"
	// SyncEmptyArtistNotManaged is the artist-scoped form of the same thing.
	SyncEmptyArtistNotManaged = "this artist is not managed by Lidarr"
)

// SyncLidarr mirrors Lidarr's albums for every Lidarr-managed artist in the collection.
// See SyncLidarrWith for what a pass does; this is the unscoped, cache-preserving form
// that the nightly run and the collection-wide button use.
func SyncLidarr(db *gorm.DB) (SyncStats, error) {
	return SyncLidarrWith(db, SyncOptions{})
}

// SyncLidarrWith mirrors Lidarr's albums for the collection's Lidarr-managed artists:
// it reads each artist's albums (with have/total track counts + monitoring) and
// records them as *catalog* state. Lidarr is authoritative about which albums exist
// and what is monitored; it is not authoritative about what is on disk, so its
// counts never touch the disk columns Rebuild owns. Where the two disagree the row
// reports a Discrepancy (see models.CollectionReleaseGroup).
//
// An artist-scoped pass exists because that is the granularity a repair actually needs:
// one album's counts going stale used to require re-mirroring every Lidarr artist in
// the collection.
func SyncLidarrWith(db *gorm.DB, opts SyncOptions) (SyncStats, error) {
	var stats SyncStats

	var managers []models.Manager
	if err := db.Where("type = ? AND enabled = ?", models.ManagerTypeLidarr, true).Find(&managers).Error; err != nil {
		return stats, err
	}
	if len(managers) == 0 {
		stats.EmptyReason = SyncEmptyNoManager
		return stats, nil
	}

	q := db.Where("managed_by IN ?", []string{models.ManagedByLidarr, models.ManagedByMixed})
	if opts.ArtistMBID != "" {
		q = q.Where("mb_id = ?", opts.ArtistMBID)
	}
	var artists []models.CollectionArtist
	if err := q.Find(&artists).Error; err != nil {
		return stats, err
	}
	want := map[string]bool{}
	for _, a := range artists {
		want[a.MBID] = true
	}
	if len(want) == 0 {
		reason, err := syncEmptyReason(db, opts)
		if err != nil {
			return stats, err
		}
		stats.EmptyReason = reason
		return stats, nil
	}

	// A manager row with no URL or key is skipped below, so a pass whose only manager
	// is unconfigured would otherwise reach the end having made no request and say
	// nothing about it.
	usable := 0
	for _, m := range managers {
		if strings.TrimSpace(m.LidarrBaseURL) != "" && strings.TrimSpace(m.LidarrAPIKey) != "" {
			usable++
		}
	}
	if usable == 0 {
		stats.EmptyReason = SyncEmptyNoManagerCredentials
		return stats, nil
	}

	for _, m := range managers {
		if strings.TrimSpace(m.LidarrBaseURL) == "" || strings.TrimSpace(m.LidarrAPIKey) == "" {
			continue
		}
		cookie := m.LidarrHeaderCookie
		client := modules.NewLidarrClient(m.LidarrBaseURL, m.LidarrAPIKey, &cookie)

		lidarrArtists, err := client.GetArtists()
		if err != nil {
			logger.Log.Warnf("failed to list Lidarr artists: %s", err.Error())
			continue
		}
		for _, la := range lidarrArtists {
			if la.ForeignArtistID == "" || !want[la.ForeignArtistID] {
				continue
			}
			albums, err := client.GetArtistAlbums(la.ID)
			if err != nil {
				logger.Log.Warnf("failed to list Lidarr albums for %s: %s", la.Name, err.Error())
				continue
			}

			// Drop this artist's cached Lidarr responses now that we hold the album IDs
			// the album and track caches are keyed by — the mapping a scoped drop needs
			// and the only place it is free. The mirror below uses none of it; the next
			// scan does.
			if opts.IgnoreCache {
				albumIDs := make([]int64, 0, len(albums))
				for _, al := range albums {
					albumIDs = append(albumIDs, al.ID)
				}
				modules.LidarrInvalidateArtistCaches(la.ID, albumIDs)
			}
			// Drop the previous catalog view for this artist so albums removed from
			// Lidarr stop being listed. Done only after the fetch succeeded, and it
			// leaves the disk columns intact.
			if err := db.Model(&models.CollectionReleaseGroup{}).
				Where("artist_mb_id = ? AND in_catalog = ?", la.ForeignArtistID, true).
				Updates(map[string]any{
					"in_catalog": false, "catalog_owned_tracks": 0,
					"catalog_total_tracks": 0, "catalog_monitored": false,
					"catalog_release_mb_id": "",
				}).Error; err != nil {
				logger.Log.Warnf("failed to reset catalog state for %s: %s", la.Name, err.Error())
			}

			for _, al := range albums {
				if al.ForeignAlbumID == "" {
					continue
				}
				// Lidarr's album type / release date; no MB secondary types here.
				_ = upsertReleaseGroup(db, rgWrite{
					mbID: al.ForeignAlbumID, artistMBID: la.ForeignArtistID, title: al.Title,
					primary: al.AlbumType, date: al.ReleaseDate,
					catalog: &catalogState{
						ownedTracks: al.Statistics.TrackFileCount,
						totalTracks: al.Statistics.TrackCount,
						monitored:   al.Monitored,
						releaseMBID: monitoredRelease(al),
					},
				})
				stats.Groups++
			}
			// Records that this artist has actually been put to a manager. It is what
			// CatalogChecked reads, so an album with no catalog row can be reported as
			// "the manager does not have this" rather than "we never asked".
			now := time.Now()
			if err := db.Model(&models.CollectionArtist{}).Where("mb_id = ?", la.ForeignArtistID).
				Update("last_synced_at", now).Error; err != nil {
				logger.Log.Warnf("failed to set last_synced_at for artist %s: %s", la.ForeignArtistID, err.Error())
			}
			stats.ArtistsSynced++
		}
	}

	// The catalog block is now current, so the wants derived from it can be. Failing
	// here is logged rather than returned: the mirror itself landed, and a sync that
	// reports failure after writing everything it fetched is the more confusing
	// outcome. The next sync reconciles again.
	if err := reconcileManagerDesires(db); err != nil {
		logger.Log.Warnf("failed to reconcile manager-derived wants: %s", err.Error())
	}
	return stats, nil
}

// syncEmptyReason works out why a mirror pass found no artist to ask Lidarr about.
// It runs only on that path, so the count it costs is paid when the answer is needed
// and never during a working pass.
//
// The distinction it draws is between an empty collection and one that simply is not
// Lidarr's. Both produce the same zero, and the fixes are opposite: the first wants
// Process (or *Add artist*), the second wants the artist's manager changed — or is
// simply correct, on a native-only install where the nightly pass will report this
// forever and should not read as a fault.
func syncEmptyReason(db *gorm.DB, opts SyncOptions) (string, error) {
	if opts.ArtistMBID != "" {
		return SyncEmptyArtistNotManaged, nil
	}
	var artists int64
	if err := db.Model(&models.CollectionArtist{}).Count(&artists).Error; err != nil {
		return "", err
	}
	if artists == 0 {
		return SyncEmptyNoArtists, nil
	}
	return SyncEmptyNoneManaged, nil
}

// monitoredRelease is the edition Lidarr selected for an album: the first release
// flagged monitored that names a MusicBrainz release. Lidarr monitors exactly one,
// but the field is a list and an unmonitored album has none, so "none" is a normal
// answer rather than a failure.
func monitoredRelease(al models.LidarrAlbum) string {
	for _, rel := range al.Releases {
		if rel.Monitored && strings.TrimSpace(rel.ForeignReleaseID) != "" {
			return strings.TrimSpace(rel.ForeignReleaseID)
		}
	}
	return ""
}

// reconcileManagerDesires records the manager's own selection as desire rows: one
// per monitored album that names a monitored release. It is the manager-side sibling
// of reconcileAutoDesires, and it exists for two reasons.
//
// The visible one: Lidarr's *album* want already reached the collection through
// catalog_monitored, but its *edition* want stopped there, so an album that is green
// in Lidarr on a specific release showed no edition wanted in Autotaggerr — the
// authority had decided and nothing said so.
//
// The structural one: a want that only exists as a mirrored column disappears with
// the mirror. As a row it is Autotaggerr's own record of what Lidarr decided, which
// is what makes detaching the manager later a change of authority rather than a loss
// of data.
//
// Three rules keep it from colliding with authored intent:
//
//   - It only ever deletes or re-points rows it owns (source = manager). A manual or
//     auto row is never touched.
//   - It skips a release-group that already carries a hand-authored want, whatever
//     edition that want names. An explicit pick outranks anything derived, and a
//     group holding both an "any" want and a specific one is the contradiction
//     SetDesire exists to prevent.
//   - It writes only for artists a manager actually owns, so a mirrored want can
//     never appear on a natively-managed artist's page.
func reconcileManagerDesires(db *gorm.DB) error {
	var artists []models.CollectionArtist
	if err := db.Where("managed_by IN ?", []string{models.ManagedByLidarr, models.ManagedByMixed}).
		Find(&artists).Error; err != nil {
		return err
	}
	managed := make(map[string]bool, len(artists))
	for _, a := range artists {
		managed[a.MBID] = true
	}

	var existing []models.CollectionDesire
	if err := db.Find(&existing).Error; err != nil {
		return err
	}
	// Hand-authored intent, per release-group: the veto on writing anything here.
	// Read across every row of the group, not just the matching edition — wanting
	// "any edition" of an album is still the user having answered this question.
	authored := map[string]bool{}
	current := map[string]models.CollectionDesire{}
	for _, d := range existing {
		if d.Source == models.DesireSourceManager {
			current[d.ReleaseGroupMBID] = d
			continue
		}
		authored[d.ReleaseGroupMBID] = true
	}

	// What the managers currently select. A row must be in the catalog, monitored,
	// and name a release for there to be an edition want at all.
	var groups []models.CollectionReleaseGroup
	if err := db.Where("in_catalog = ? AND catalog_monitored = ? AND catalog_release_mb_id <> ''", true, true).
		Find(&groups).Error; err != nil {
		return err
	}

	// Through the credit links, not artist_mb_id alone: a collaboration is stored
	// under its primary credit, which need not be the Lidarr artist — the same reason
	// every other per-artist read here is keyed by release-group.
	rgMBIDs := make([]string, 0, len(groups))
	for _, rg := range groups {
		rgMBIDs = append(rgMBIDs, rg.MBID)
	}
	credits, err := ArtistsByReleaseGroup(db, rgMBIDs)
	if err != nil {
		return err
	}

	type managerWant struct {
		rg         models.CollectionReleaseGroup
		artistMBID string
	}
	wanted := map[string]managerWant{}
	for _, rg := range groups {
		if authored[rg.MBID] {
			continue
		}
		// The row belongs to the managed artist credited on it, so the desire is
		// recorded against the page that can explain where it came from.
		owner := ""
		for _, artistMBID := range CreditedArtists(rg, credits) {
			if managed[artistMBID] {
				owner = artistMBID
				break
			}
		}
		if owner == "" {
			continue
		}
		wanted[rg.MBID] = managerWant{rg: rg, artistMBID: owner}
	}

	// Prune first, so a re-point (Lidarr's selection moved) frees the group's row
	// before the create below re-adds it at the new edition.
	for rgMBID, d := range current {
		want, keep := wanted[rgMBID]
		if keep && want.rg.CatalogReleaseMBID == d.ReleaseMBID && want.artistMBID == d.ArtistMBID {
			continue
		}
		if err := db.Delete(&models.CollectionDesire{}, "id = ?", d.ID).Error; err != nil {
			return err
		}
		delete(current, rgMBID)
	}

	for rgMBID, want := range wanted {
		if _, have := current[rgMBID]; have {
			continue
		}
		if err := db.Create(&models.CollectionDesire{
			ArtistMBID:       want.artistMBID,
			ReleaseGroupMBID: rgMBID,
			ReleaseMBID:      want.rg.CatalogReleaseMBID,
			Source:           models.DesireSourceManager,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}
