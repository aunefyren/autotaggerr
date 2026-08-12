package migration

import (
	"fmt"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

// The review payload: what a pending migration actually means, in the words a person
// deciding on it needs.
//
// The stored row cannot answer that on its own. It holds two MBIDs, a kind and an
// impact snapshot, and for a release-group deletion — the commonest row by far on a
// manager-fed install — every one of those reads as a zero: no new ID (a deletion has
// none), no affected files (files are keyed by release, never by release-group), no
// affected desires in the ordinary case. Rendered literally that is "Album · ID does
// not resolve" with nothing under it, which states the symptom and answers none of the
// three questions the reader has: what is wrong, do I own this, and what happens if I
// press approve.
//
// So the answers are computed here, at read time, rather than stored. They depend on
// state that moves independently of the migration — files arrive, a manager stops
// listing an album, a repair is attempted — and a snapshot taken at detection would be
// confidently wrong by the time anybody looked at it.

// Review is a migration plus the context needed to decide on it.
//
// The structured fields are the contract; Problem and Effect are the same facts
// written out, so a client can render either without having to know the rules that
// distinguish a merge from a deletion or a retirable album from a blocked one.
type Review struct {
	models.MusicbrainzMigration

	// ArtistMBID and ArtistName name the artist to act on. For a release-group row
	// this is the artist a manager refresh would target, which is the fix the
	// blocked case asks for — a bare album title does not tell the user where to go.
	ArtistMBID string `json:"artist_mb_id,omitempty"`
	ArtistName string `json:"artist_name,omitempty"`

	// FilesOnDisk is how many indexed files sit under this entity. It is *not*
	// AffectedFiles: a retirement rewrites no file at all, and the honest answer to
	// "do I have files for this?" is a different number from "how many files would
	// this rewrite". Conflating them is what made a release-group row report zero
	// while the album was sitting on disk.
	FilesOnDisk int `json:"files_on_disk"`
	// Editions is the owned-edition rows under a release-group, so "12 files across
	// 2 editions" is sayable.
	Editions int `json:"editions,omitempty"`

	// Owned and InCatalog are the two authorities that can object to a retirement:
	// files on disk, and a manager still listing the album.
	Owned     bool `json:"owned"`
	InCatalog bool `json:"in_catalog"`

	// Blocker is why approving would not complete right now, empty when nothing
	// objects. It is the same sentence the apply path would fail with, read before
	// the fact instead of after.
	Blocker string `json:"blocker,omitempty"`

	// NeedsManagerRefresh marks the one blocker that is not final: the manager still
	// lists the album, which a refresh of that artist may well change. It is what
	// tells the UI that approve will ask the manager rather than apply.
	NeedsManagerRefresh bool `json:"needs_manager_refresh"`

	// Problem is what is wrong, and Effect is what approving does about it — both
	// plain sentences, both safe to show verbatim.
	Problem string `json:"problem"`
	Effect  string `json:"effect"`
}

// Reviews decorates a list of migrations for the review UI.
func Reviews(db *gorm.DB, rows []models.MusicbrainzMigration) []Review {
	out := make([]Review, 0, len(rows))
	for _, row := range rows {
		out = append(out, NewReview(db, row))
	}
	return out
}

// NewReview computes the review context for one migration.
//
// Every lookup here is best-effort: a missing collection row means the entity is
// already gone, which is information rather than an error, and a review that failed
// to render because one count could not be read would be worse than one that renders
// with the count absent.
func NewReview(db *gorm.DB, m models.MusicbrainzMigration) Review {
	r := Review{MusicbrainzMigration: m}
	if db == nil {
		return r
	}

	switch m.EntityType {
	case models.MigrationEntityReleaseGroup:
		r.fillReleaseGroup(db)
	case models.MigrationEntityArtist:
		r.ArtistMBID = m.OldMBID
		r.ArtistName = m.Name
		r.FilesOnDisk = filesUnderArtist(db, m.OldMBID)
	default:
		r.FilesOnDisk = m.AffectedFiles
		var release models.CollectionRelease
		if err := db.Where("mb_id = ?", m.OldMBID).First(&release).Error; err == nil {
			r.ArtistMBID = release.ArtistMBID
			r.ArtistName = artistName(db, release.ArtistMBID)
		}
	}

	r.Problem, r.Effect = r.sentences()
	return r
}

// fillReleaseGroup reads the album's own row plus the blocker that decides what
// approving will do.
func (r *Review) fillReleaseGroup(db *gorm.DB) {
	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", r.OldMBID).First(&rg).Error; err != nil {
		// Already retired, by an earlier approval or an artist prune. Nothing objects
		// to a retirement that has happened, so the row is left with empty context and
		// a blocker of "" — which is the truth.
		return
	}

	r.ArtistMBID = rg.ArtistMBID
	r.ArtistName = artistName(db, rg.ArtistMBID)
	r.Owned = rg.Owned
	r.InCatalog = rg.InCatalog
	r.Editions = countRows(db, &models.CollectionRelease{}, "release_group_mb_id = ?", r.OldMBID)
	r.FilesOnDisk = filesUnderReleaseGroup(db, r.OldMBID)

	// Asked of collection rather than re-derived, for the same reason the apply path
	// asks: two implementations of "may this be retired" would eventually disagree,
	// and the one shown to the user would be the wrong one to trust.
	retirable, reason, err := collection.ReleaseGroupRetirable(db, r.OldMBID)
	if err != nil {
		return
	}
	if !retirable {
		r.Blocker = reason
		// Only the catalog objection is repairable. Files on disk and an authored want
		// are objections a manager refresh cannot answer.
		r.NeedsManagerRefresh = rg.InCatalog && !rg.Owned
	}
}

// sentences writes Problem and Effect for this migration.
//
// One switch rather than sentences scattered through the fill functions, so the
// wording of the four cases can be read against each other — they are the whole
// user-facing surface of this feature.
func (r Review) sentences() (problem, effect string) {
	files := plural(r.FilesOnDisk, "file", "files")

	switch {
	case r.EntityType == models.MigrationEntityReleaseGroup:
		problem = "MusicBrainz does not have this album under the ID Autotaggerr holds. " +
			"That is usually a manager holding an ID its metadata service has since " +
			"dropped or re-keyed, rather than the album being gone — so the ID is " +
			"checked with the manager before anything is removed."
		problem += " " + r.diskSentence()

		switch {
		case r.NeedsManagerRefresh:
			who := "the manager"
			if r.ArtistName != "" {
				who = fmt.Sprintf("the manager to re-read %s", r.ArtistName)
			}
			effect = fmt.Sprintf("Approving asks %s. If the album is mis-keyed the manager "+
				"corrects the ID and this entry resolves itself; if the manager stops "+
				"listing the album, it is removed from your collection. No files are "+
				"touched and nothing is deleted from the manager either way.", who)
		case r.Blocker != "":
			effect = "Approving would not remove anything right now: " + r.Blocker + "."
		default:
			effect = "Approving removes the album from your collection view. " +
				"No files are touched, and nothing is deleted from the manager."
		}
		return problem, effect

	case r.Kind == models.MigrationKindDeleted && r.EntityType == models.MigrationEntityArtist:
		problem = "MusicBrainz no longer has this artist under the ID Autotaggerr holds."
		effect = "Approving removes the artist from your collection. Their albums are " +
			"keyed by release, so no file and no album row is affected."
		return problem, effect

	case r.Kind == models.MigrationKindDeleted:
		problem = "MusicBrainz no longer has this release under the ID Autotaggerr holds."
		effect = fmt.Sprintf("Approving marks %s as needing re-identification and drops the "+
			"owned edition, so the album stops counting as complete. The MusicBrainz IDs "+
			"stay on the files, and any want you authored is left alone.", files)
		return problem, effect

	case r.EntityType == models.MigrationEntityArtist:
		problem = "MusicBrainz merged this artist into another. The collection is keyed " +
			"on an ID that now names a different record."
		effect = "Approving re-points the artist's albums, editions and wants at the " +
			"surviving ID. Monitoring and follow settings are merged, never dropped. " +
			"No files are touched."
		return problem, effect

	default:
		problem = "MusicBrainz merged this release into another. The collection is keyed " +
			"on an ID that now names a different record."
		effect = fmt.Sprintf("Approving re-points %s at the surviving ID and clears their "+
			"processed marker, so the next run re-reads track IDs from the new release "+
			"and re-tags them.", plural(r.AffectedFiles, "file", "files"))
		if r.AffectedDesires > 0 {
			effect += fmt.Sprintf(" %s follows, and duplicates collapse into one.",
				plural(r.AffectedDesires, "want", "wants"))
		}
		if r.TouchesPinned {
			effect += " One of those files is a manual attachment; a merge renames the " +
				"release rather than substituting a different one, so the pin follows it."
		}
		return problem, effect
	}
}

// diskSentence answers "do I have files for this?" — the question a retirement row
// most needs to answer and the stored row cannot.
func (r Review) diskSentence() string {
	if r.FilesOnDisk == 0 {
		return "You have no files under this album."
	}
	s := fmt.Sprintf("You have %s under this album", plural(r.FilesOnDisk, "file", "files"))
	if r.Editions > 1 {
		s += fmt.Sprintf(", across %d editions", r.Editions)
	}
	return s + "."
}

// filesUnderReleaseGroup counts indexed files whose release belongs to this album.
// Files are keyed by release, so the album's editions are the only route from one to
// the other.
func filesUnderReleaseGroup(db *gorm.DB, releaseGroupMBID string) int {
	var editions []string
	if err := db.Model(&models.CollectionRelease{}).
		Where("release_group_mb_id = ?", releaseGroupMBID).
		Distinct().Pluck("mb_id", &editions).Error; err != nil || len(editions) == 0 {
		return 0
	}
	return countRows(db, &models.LibraryItem{}, "mb_release_id IN ?", editions)
}

// filesUnderArtist counts indexed files under any edition credited to this artist.
func filesUnderArtist(db *gorm.DB, artistMBID string) int {
	var editions []string
	if err := db.Model(&models.CollectionRelease{}).
		Where("artist_mb_id = ?", artistMBID).
		Distinct().Pluck("mb_id", &editions).Error; err != nil || len(editions) == 0 {
		return 0
	}
	return countRows(db, &models.LibraryItem{}, "mb_release_id IN ?", editions)
}

func artistName(db *gorm.DB, artistMBID string) string {
	if artistMBID == "" {
		return ""
	}
	var artist models.CollectionArtist
	if err := db.Where("mb_id = ?", artistMBID).First(&artist).Error; err != nil {
		return ""
	}
	return artist.Name
}

func countRows(db *gorm.DB, model any, query string, args ...any) int {
	var n int64
	if err := db.Model(model).Where(query, args...).Count(&n).Error; err != nil {
		return 0
	}
	return int(n)
}

// plural writes "1 file" / "2 files" — a count the reader can put in a sentence
// without it reading as machine output.
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
