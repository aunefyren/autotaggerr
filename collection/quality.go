package collection

import (
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/quality"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// QualityIndex is what judging albums' quality needs, read once for a page: the
// quality profiles, which library uses which, and the measured quality of every copy
// of the albums asked about. Facts and policy are kept apart on purpose — the copies'
// qualities are read from the index as they stand, and the profile is applied at read
// time, so editing a profile changes every page at once with no rebuild. See
// docs/quality.md.
type QualityIndex struct {
	libraries      LibraryIndex
	profiles       map[uuid.UUID]models.QualityProfile
	libraryProfile map[uuid.UUID]uuid.UUID
	// copies: album → library → edition → quality → files.
	copies map[string]map[uuid.UUID]map[string]map[quality.ID]int
}

// LoadQualityIndex reads the profiles, the libraries' choices of them, and the
// measured qualities of the copies of the albums named (nil: every album).
func LoadQualityIndex(db *gorm.DB, libraries LibraryIndex, releaseGroupMBIDs []string) (QualityIndex, error) {
	q := QualityIndex{
		libraries:      libraries,
		profiles:       map[uuid.UUID]models.QualityProfile{},
		libraryProfile: map[uuid.UUID]uuid.UUID{},
		copies:         map[string]map[uuid.UUID]map[string]map[quality.ID]int{},
	}
	var profiles []models.QualityProfile
	if err := db.Find(&profiles).Error; err != nil {
		return q, err
	}
	for _, p := range profiles {
		q.profiles[p.ID] = p
	}
	if len(profiles) == 0 {
		return q, nil // nothing can be judged: skip reading the copies
	}
	var libs []models.Library
	if err := db.Select("id", "quality_profile_id").Where("quality_profile_id IS NOT NULL").Find(&libs).Error; err != nil {
		return q, err
	}
	for _, l := range libs {
		q.libraryProfile[l.ID] = *l.QualityProfileID
	}

	if releaseGroupMBIDs != nil && len(releaseGroupMBIDs) == 0 {
		return q, nil
	}
	// The copies table says which (edition, library) pairs the collection counts, so
	// a file outside the disk view does not decide an album's quality.
	type row struct {
		ReleaseGroupMBID string
		LibraryID        uuid.UUID
		ReleaseMBID      string
		Quality          string
		Files            int
	}
	read := func(groups []string) error {
		query := db.Table("collection_release_copies AS c").
			Select("c.release_group_mb_id, c.library_id, c.release_mb_id, i.quality, count(*) AS files").
			Joins("JOIN library_items AS i ON i.mb_release_id = c.release_mb_id AND i.library_id = c.library_id").
			Where("i.quality <> ''").
			Group("c.release_group_mb_id, c.library_id, c.release_mb_id, i.quality")
		if groups != nil {
			query = query.Where("c.release_group_mb_id IN ?", groups)
		}
		var rows []row
		if err := query.Scan(&rows).Error; err != nil {
			return err
		}
		for _, r := range rows {
			byLibrary := q.copies[r.ReleaseGroupMBID]
			if byLibrary == nil {
				byLibrary = map[uuid.UUID]map[string]map[quality.ID]int{}
				q.copies[r.ReleaseGroupMBID] = byLibrary
			}
			byEdition := byLibrary[r.LibraryID]
			if byEdition == nil {
				byEdition = map[string]map[quality.ID]int{}
				byLibrary[r.LibraryID] = byEdition
			}
			counts := byEdition[r.ReleaseMBID]
			if counts == nil {
				counts = map[quality.ID]int{}
				byEdition[r.ReleaseMBID] = counts
			}
			counts[quality.ID(r.Quality)] += r.Files
		}
		return nil
	}
	if releaseGroupMBIDs == nil {
		return q, read(nil)
	}
	for _, chunk := range chunked(releaseGroupMBIDs) {
		if err := read(chunk); err != nil {
			return q, err
		}
	}
	return q, nil
}

// QualityProfileFor is the one definition of which profile judges an artist's copies
// in a library: none unless Autotaggerr decides there (Lidarr decides quality in its
// own libraries), then the artist's override, then the library's own. A profile that
// no longer exists counts as none. A future grabber asks the same question.
func (q QualityIndex) QualityProfileFor(artist models.CollectionArtist, library uuid.UUID) (models.QualityProfile, bool) {
	if q.libraries.Authority(artist, library) != AuthorityNative {
		return models.QualityProfile{}, false
	}
	if artist.QualityProfileID != nil {
		if p, ok := q.profiles[*artist.QualityProfileID]; ok {
			return p, true
		}
	}
	if id, ok := q.libraryProfile[library]; ok {
		if p, ok := q.profiles[id]; ok {
			return p, true
		}
	}
	return models.QualityProfile{}, false
}

// AlbumQuality is an album's copies judged against the profile that applies.
type AlbumQuality struct {
	// Known is false when no profile applies or no copy was measured; the rest is
	// then empty, and nothing is flagged.
	Known     bool       `json:"known"`
	LibraryID uuid.UUID  `json:"library_id"`
	ProfileID uuid.UUID  `json:"profile_id"`
	Profile   string     `json:"profile"`
	CutoffMet bool       `json:"cutoff_met"`
	Lowest    quality.ID `json:"lowest"`
	// LowestName and CutoffName are display names: "MP3-320", "FLAC".
	LowestName     string `json:"lowest_name"`
	CutoffName     string `json:"cutoff_name"`
	UpgradeAllowed bool   `json:"upgrade_allowed"`
}

// NeedsUpgrade reports whether the copies are below a cutoff the profile wants
// upgraded to.
func (a AlbumQuality) NeedsUpgrade() bool {
	return a.Known && !a.CutoffMet && a.UpgradeAllowed
}

// JudgeAlbum judges an album's copies for an artist, in one library or (uuid.Nil)
// across every library a profile applies in.
//
// Within a library the album is good enough when **one edition** is: every measured
// file of it meets the cutoff — the rule an album want is satisfied by. Two editions,
// one FLAC and one MP3, do not ask for an upgrade. Across libraries, the verdict is
// the first library still asking for one, else the first that was judged, so the
// overview flags an album any library's profile wants better.
func (q QualityIndex) JudgeAlbum(artist models.CollectionArtist, releaseGroupMBID string, library uuid.UUID) AlbumQuality {
	byLibrary := q.copies[releaseGroupMBID]
	if library != uuid.Nil {
		return q.judgeIn(artist, library, byLibrary[library])
	}
	var first AlbumQuality
	for _, l := range q.libraries.libraries {
		editions, ok := byLibrary[l.ID]
		if !ok {
			continue
		}
		verdict := q.judgeIn(artist, l.ID, editions)
		if verdict.NeedsUpgrade() {
			return verdict
		}
		if verdict.Known && !first.Known {
			first = verdict
		}
	}
	return first
}

// judgeIn judges one library's copies: the best edition decides.
func (q QualityIndex) judgeIn(artist models.CollectionArtist, library uuid.UUID, editions map[string]map[quality.ID]int) AlbumQuality {
	if len(editions) == 0 {
		return AlbumQuality{}
	}
	row, ok := q.QualityProfileFor(artist, library)
	if !ok {
		return AlbumQuality{}
	}
	profile := row.Profile()
	verdict := AlbumQuality{
		LibraryID: library, ProfileID: row.ID, Profile: row.Name,
		UpgradeAllowed: profile.UpgradeAllowed, CutoffName: tierName(profile.CutoffQualities()),
	}
	bestRank := 0
	for _, counts := range editions {
		have := make([]quality.ID, 0, len(counts))
		for id := range counts {
			have = append(have, id)
		}
		met, lowest, known := profile.AlbumCutoff(have)
		if !known {
			continue
		}
		rank := profile.Rank(lowest)
		better := !verdict.Known || (met && !verdict.CutoffMet) || (met == verdict.CutoffMet && rank > bestRank)
		if better {
			verdict.Known, verdict.CutoffMet, verdict.Lowest, bestRank = true, met, lowest, rank
		}
	}
	if !verdict.Known {
		return AlbumQuality{}
	}
	verdict.LowestName = quality.Name(verdict.Lowest)
	return verdict
}

// tierName names a tier for a sentence: "FLAC" or "FLAC / FLAC 24bit".
func tierName(ids []quality.ID) string {
	name := ""
	for i, id := range ids {
		if i > 0 {
			name += " / "
		}
		name += quality.Name(id)
	}
	return name
}
