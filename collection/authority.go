package collection

import (
	"errors"
	"strings"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Authority is decided per library, not per artist. An artist with files in a
// Lidarr library and in a native one has two authorities: Lidarr decides what is
// wanted and which edition for its own library, and Autotaggerr decides for the
// other. CollectionArtist.ManagedBy is still the artist-wide summary ("mixed" when
// both apply), and it is still what the Lidarr mirror reads to decide whom to ask —
// but nothing that writes a want or a follow may read it as "Lidarr owns all of it".
//
// The authorities a library can have, for one artist.
const (
	AuthorityLidarr  = models.ManagedByLidarr
	AuthorityNative  = models.ManagedByAutotaggerr
	AuthorityUnknown = models.ManagedByUnknown
)

// Why a want or a follow could not be placed. The routers turn each into its own
// sentence; they are errors here because the rule that produces them is here.
var (
	// ErrLibraryManaged: the library named is one Lidarr decides for.
	ErrLibraryManaged = errors.New("that library is managed by Lidarr")
	// ErrChooseLibrary: Lidarr governs the artist and more than one native library
	// could take the want, so the caller has to say which.
	ErrChooseLibrary = errors.New("choose which library this is for")
	// ErrNoNativeLibrary: Lidarr governs the artist and no library is native.
	ErrNoNativeLibrary = errors.New("no library Autotaggerr decides for")
	// ErrUnknownLibrary: no such library.
	ErrUnknownLibrary = errors.New("library not found")
)

// GovernedByLidarr reports whether Lidarr decides for any of this artist's files. A
// detached artist is not: detaching holds ManagedBy at native.
func GovernedByLidarr(artist models.CollectionArtist) bool {
	return artist.ManagedBy == models.ManagedByLidarr || artist.ManagedBy == models.ManagedByMixed
}

// LibraryAuthority is who decides for this artist's files in a library whose
// manager has the given type (see libraryManagerTypes). Detaching an artist takes
// every one of its libraries native, which is what detach has always meant.
func LibraryAuthority(artist models.CollectionArtist, managerType string) string {
	switch managerType {
	case models.ManagerTypeLidarr:
		if artist.ManagerDetached {
			return AuthorityNative
		}
		return AuthorityLidarr
	case models.ManagerTypeAutotaggerr:
		return AuthorityNative
	default:
		return AuthorityUnknown
	}
}

// DesireInScope reports whether a want belongs to the view of one library. A want
// for that library does; a want tied to no library belongs to the artist's default
// authority (see models.CollectionDesire.LibraryID), so it shows under Lidarr's
// library when Lidarr governs the artist and under every native library otherwise.
// uuid.Nil as the library is the overview, which every want belongs to.
func DesireInScope(d models.CollectionDesire, artist models.CollectionArtist, library uuid.UUID, authority string) bool {
	if library == uuid.Nil || d.LibraryID == library {
		return true
	}
	if d.LibraryID != uuid.Nil {
		return false
	}
	switch authority {
	case AuthorityLidarr:
		return GovernedByLidarr(artist)
	case AuthorityNative:
		// A manager-derived want left on an artist no manager governs any more is
		// still shown, so it can be dismissed where it is seen.
		return !GovernedByLidarr(artist)
	}
	return false
}

// FollowGoverns reports whether the *native* follow settings decide what is wanted
// anywhere for this artist. They do for an artist no manager governs, and for one
// Lidarr governs only once following has been pointed at a native library.
//
// Without the second rule, a stale Monitored flag — set before the artist became
// Lidarr-managed, or by the old artist-level "Monitored" toggle — kept producing
// automatic wants on a page that offered no follow control to turn them off. The
// state was real, but nothing on screen could explain or change it. A flag with no
// library named stays recorded and governs nothing until the artist is natively
// managed again, or followed in a native library.
func FollowGoverns(artist models.CollectionArtist) bool {
	return !GovernedByLidarr(artist) || artist.FollowLibraryID != uuid.Nil
}

// FollowAppliesIn reports whether following decides what is wanted in one native
// library: the library it was pointed at, or — for an artist no manager governs,
// followed without naming one — every native library.
func FollowAppliesIn(artist models.CollectionArtist, library uuid.UUID) bool {
	if artist.FollowLibraryID != uuid.Nil {
		return artist.FollowLibraryID == library
	}
	return !GovernedByLidarr(artist)
}

// DetachedReleases reports which of these editions belong to a detached artist — the
// album's artist, by the same primary credit Rebuild files it under. Their files in a
// Lidarr library are Autotaggerr's to identify: LibraryAuthority takes a detached
// artist's every library native, and a file's identity follows the same rule as its
// album's wants. An edition the collection does not know, or one with no artist, is
// absent: with no artist to have been detached, the library's manager still decides.
func DetachedReleases(db *gorm.DB, releaseMBIDs []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(releaseMBIDs) == 0 {
		return out, nil
	}
	var ids []string
	if err := db.Model(&models.CollectionRelease{}).
		Joins("JOIN collection_artists ON collection_artists.mb_id = collection_releases.artist_mb_id").
		Where("collection_releases.mb_id IN ? AND collection_artists.manager_detached = ?", releaseMBIDs, true).
		Pluck("collection_releases.mb_id", &ids).Error; err != nil {
		return nil, err
	}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// LibraryIndex is the libraries and who manages each, read once so authority can be
// answered for a whole collection list without a query per artist.
type LibraryIndex struct {
	types     map[uuid.UUID]string
	libraries []models.Library // ordered by name
}

// LoadLibraryIndex reads every library and its manager's type.
func LoadLibraryIndex(db *gorm.DB) (LibraryIndex, error) {
	types, err := libraryManagerTypes(db)
	if err != nil {
		return LibraryIndex{}, err
	}
	var libraries []models.Library
	if err := db.Select("id", "name", "enabled").Order("name").Find(&libraries).Error; err != nil {
		return LibraryIndex{}, err
	}
	return LibraryIndex{types: types, libraries: libraries}, nil
}

// Exists reports whether a library of this ID is configured.
func (x LibraryIndex) Exists(library uuid.UUID) bool {
	_, ok := x.types[library]
	return ok
}

// Find resolves a library by ID or, ignoring case, by name.
func (x LibraryIndex) Find(nameOrID string) (uuid.UUID, bool) {
	nameOrID = strings.TrimSpace(nameOrID)
	if id, err := uuid.Parse(nameOrID); err == nil && x.Exists(id) {
		return id, true
	}
	for _, l := range x.libraries {
		if strings.EqualFold(l.Name, nameOrID) {
			return l.ID, true
		}
	}
	return uuid.Nil, false
}

// Authority is LibraryAuthority for a library by ID; AuthorityUnknown when there is
// no such library.
func (x LibraryIndex) Authority(artist models.CollectionArtist, library uuid.UUID) string {
	return LibraryAuthority(artist, x.types[library])
}

// native lists the enabled libraries Autotaggerr decides for, for this artist.
func (x LibraryIndex) native(artist models.CollectionArtist) []uuid.UUID {
	var out []uuid.UUID
	for _, l := range x.libraries {
		if l.Enabled && x.Authority(artist, l.ID) == AuthorityNative {
			out = append(out, l.ID)
		}
	}
	return out
}

// HasNativeLibrary reports whether any library is one Autotaggerr decides for
// whatever the artist: a library with no manager, or the native one. A disabled one
// counts — it is switched off, not gone.
func (x LibraryIndex) HasNativeLibrary() bool {
	for _, l := range x.libraries {
		if x.types[l.ID] == models.ManagerTypeAutotaggerr {
			return true
		}
	}
	return false
}

// WantTarget resolves which library a want or a follow is written for. Named, the
// library must be one Autotaggerr decides for this artist. Unnamed, an artist no
// manager governs gets uuid.Nil — every native library, as wants always meant — and
// an artist Lidarr governs gets the one native library there is, so a page showing
// all libraries can still edit where there is only one place an edit can go.
func (x LibraryIndex) WantTarget(artist models.CollectionArtist, library *uuid.UUID) (uuid.UUID, error) {
	if library != nil && *library != uuid.Nil {
		if !x.Exists(*library) {
			return uuid.Nil, ErrUnknownLibrary
		}
		if x.Authority(artist, *library) != AuthorityNative {
			return uuid.Nil, ErrLibraryManaged
		}
		return *library, nil
	}
	if !GovernedByLidarr(artist) {
		return uuid.Nil, nil
	}
	native := x.native(artist)
	switch len(native) {
	case 0:
		return uuid.Nil, ErrNoNativeLibrary
	case 1:
		return native[0], nil
	default:
		return uuid.Nil, ErrChooseLibrary
	}
}

// WantTarget is LibraryIndex.WantTarget with the index read for the one call.
func WantTarget(db *gorm.DB, artist models.CollectionArtist, library *uuid.UUID) (uuid.UUID, error) {
	idx, err := LoadLibraryIndex(db)
	if err != nil {
		return uuid.Nil, err
	}
	return idx.WantTarget(artist, library)
}

// ArtistLibrary is one library as an artist's pages see it.
type ArtistLibrary struct {
	LibraryID uuid.UUID `json:"library_id"`
	Name      string    `json:"name"`
	// Authority is who decides for this artist here: AuthorityLidarr,
	// AuthorityNative or AuthorityUnknown.
	Authority string `json:"authority"`
	// Holds is whether any of the artist's albums has files here.
	Holds bool `json:"holds"`
}

// ArtistLibraries lists the libraries an artist can be viewed and edited in: every
// library holding their files, and every enabled native library besides, since a
// native library can be wanted into before it holds anything. A Lidarr library that
// holds nothing of theirs is left out — Lidarr's opinion of an artist reaches the
// collection only through its files. Ordered by name.
func (x LibraryIndex) ArtistLibraries(db *gorm.DB, artist models.CollectionArtist, releaseGroupMBIDs []string) ([]ArtistLibrary, error) {
	holds := map[uuid.UUID]bool{}
	if len(releaseGroupMBIDs) > 0 {
		var ids []uuid.UUID
		if err := db.Model(&models.CollectionReleaseCopy{}).
			Where("release_group_mb_id IN ?", releaseGroupMBIDs).
			Distinct("library_id").Pluck("library_id", &ids).Error; err != nil {
			return nil, err
		}
		for _, id := range ids {
			holds[id] = true
		}
	}
	out := []ArtistLibrary{}
	for _, l := range x.libraries {
		authority := x.Authority(artist, l.ID)
		if !holds[l.ID] && (authority != AuthorityNative || !l.Enabled) {
			continue
		}
		out = append(out, ArtistLibrary{LibraryID: l.ID, Name: l.Name, Authority: authority, Holds: holds[l.ID]})
	}
	return out, nil
}

// Holding is what one library holds of an album: its best edition there, by the
// same rule the album's own summary uses.
type Holding struct {
	OwnedTracks int
	TotalTracks int
	// Editions is how many editions the library holds files of.
	Editions int
}

// LibraryHoldings reports, per album, what one library holds of it. An album the
// library holds nothing of is absent.
func LibraryHoldings(db *gorm.DB, library uuid.UUID, releaseGroupMBIDs []string) (map[string]Holding, error) {
	out := map[string]Holding{}
	if len(releaseGroupMBIDs) == 0 {
		return out, nil
	}
	var copies []models.CollectionReleaseCopy
	if err := db.Where("library_id = ? AND release_group_mb_id IN ?", library, releaseGroupMBIDs).
		Find(&copies).Error; err != nil {
		return nil, err
	}
	if len(copies) == 0 {
		return out, nil
	}
	releaseIDs := make([]string, 0, len(copies))
	for _, c := range copies {
		releaseIDs = append(releaseIDs, c.ReleaseMBID)
	}
	var releases []models.CollectionRelease
	if err := db.Select("mb_id", "total_tracks").Where("mb_id IN ?", releaseIDs).Find(&releases).Error; err != nil {
		return nil, err
	}
	totals := make(map[string]int, len(releases))
	for _, r := range releases {
		totals[r.MBID] = r.TotalTracks
	}
	for _, c := range copies {
		h := out[c.ReleaseGroupMBID]
		h.Editions++
		if c.OwnedTracks > h.OwnedTracks {
			h.OwnedTracks, h.TotalTracks = c.OwnedTracks, totals[c.ReleaseMBID]
		}
		out[c.ReleaseGroupMBID] = h
	}
	return out, nil
}
