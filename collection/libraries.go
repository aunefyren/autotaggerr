package collection

import (
	"sort"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// LibraryCopy is what one library holds of an edition or an album. For an album it
// describes the library's best-owned edition, the same rule the album's own summary
// uses, so a row reading "Music · 12/12" means one edition is complete there.
type LibraryCopy struct {
	LibraryID   uuid.UUID `json:"library_id"`
	Name        string    `json:"name"`
	OwnedTracks int       `json:"owned_tracks"`
	Files       int       `json:"files"`
}

// ReleaseCopies lists, per edition, the libraries holding files of it.
func ReleaseCopies(db *gorm.DB, releaseMBIDs []string) (map[string][]LibraryCopy, error) {
	out := map[string][]LibraryCopy{}
	if len(releaseMBIDs) == 0 {
		return out, nil
	}
	var rows []models.CollectionReleaseCopy
	if err := db.Where("release_mb_id IN ?", releaseMBIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	names, err := libraryNames(db)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ReleaseMBID] = append(out[r.ReleaseMBID], LibraryCopy{
			LibraryID: r.LibraryID, Name: names[r.LibraryID], OwnedTracks: r.OwnedTracks, Files: r.Files,
		})
	}
	for k := range out {
		sortCopies(out[k])
	}
	return out, nil
}

// ReleaseGroupCopies lists, per album, the libraries holding any edition of it.
// Files sum over the library's editions; OwnedTracks is its best edition's.
func ReleaseGroupCopies(db *gorm.DB, releaseGroupMBIDs []string) (map[string][]LibraryCopy, error) {
	out := map[string][]LibraryCopy{}
	if len(releaseGroupMBIDs) == 0 {
		return out, nil
	}
	var rows []models.CollectionReleaseCopy
	if err := db.Where("release_group_mb_id IN ?", releaseGroupMBIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	names, err := libraryNames(db)
	if err != nil {
		return nil, err
	}
	type key struct {
		group   string
		library uuid.UUID
	}
	merged := map[key]*LibraryCopy{}
	for _, r := range rows {
		k := key{r.ReleaseGroupMBID, r.LibraryID}
		c := merged[k]
		if c == nil {
			c = &LibraryCopy{LibraryID: r.LibraryID, Name: names[r.LibraryID]}
			merged[k] = c
		}
		c.Files += r.Files
		c.OwnedTracks = max(c.OwnedTracks, r.OwnedTracks)
	}
	for k, c := range merged {
		out[k.group] = append(out[k.group], *c)
	}
	for k := range out {
		sortCopies(out[k])
	}
	return out, nil
}

// LibrariesByReleaseGroup is the set of libraries holding each album, for the
// collection list, which needs only membership and not the counts.
func LibrariesByReleaseGroup(db *gorm.DB) (map[string][]uuid.UUID, error) {
	var rows []struct {
		ReleaseGroupMBID string
		LibraryID        uuid.UUID
	}
	if err := db.Model(&models.CollectionReleaseCopy{}).
		Distinct("release_group_mb_id", "library_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := map[string][]uuid.UUID{}
	for _, r := range rows {
		out[r.ReleaseGroupMBID] = append(out[r.ReleaseGroupMBID], r.LibraryID)
	}
	return out, nil
}

func libraryNames(db *gorm.DB) (map[uuid.UUID]string, error) {
	var libraries []models.Library
	if err := db.Select("id", "name").Find(&libraries).Error; err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]string, len(libraries))
	for _, l := range libraries {
		out[l.ID] = l.Name
	}
	return out, nil
}

// sortCopies orders by library name, then ID, so a page lists libraries the same
// way on every row.
func sortCopies(copies []LibraryCopy) {
	sort.Slice(copies, func(i, j int) bool {
		if copies[i].Name != copies[j].Name {
			return copies[i].Name < copies[j].Name
		}
		return copies[i].LibraryID.String() < copies[j].LibraryID.String()
	})
}
