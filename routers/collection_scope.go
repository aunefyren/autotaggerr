package routers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
)

// viewScope is the library an artist's pages are read in. Authority is per library
// (see collection/authority.go), so an artist Lidarr governs in one library and
// Autotaggerr in another reads differently in each: under Lidarr's library the wants
// are Lidarr's and frozen, under the native one they are the user's to edit, and the
// ownership on every row is that library's own.
//
// The zero library is the overview — every library at once, the page as it was
// before there was a choice. For an artist no manager governs it edits as it always
// did. For one Lidarr governs it edits nothing: its rows mix Lidarr's wants with a
// native library's, so an edit there could not mean what the row shows — untick
// Lidarr's edition and only the native want would go. Choosing the library is the
// edit. (The API still takes an unnamed want for such an artist, into the one native
// library there is — see WantTarget — since a caller with no page has no tab.)
type viewScope struct {
	library   uuid.UUID
	authority string
	// editable is whether wants and editions may be written in this view, and
	// target the library they are written for (uuid.Nil: tied to none).
	editable bool
	target   uuid.UUID
	// blocked says why not, when not: "lidarr", "unknown", "choose_library" or
	// "no_native_library". The UI turns it into a sentence.
	blocked string
	// holdings is what the library holds of each album; nil for the overview.
	holdings map[string]collection.Holding
	// followingIn is the native libraries following wants into, whatever the view.
	followingIn []uuid.UUID
}

func (s viewScope) overview() bool { return s.library == uuid.Nil }

// errBadLibrary is the 400 for a library parameter that is not an ID.
var errBadLibrary = failWith(http.StatusBadRequest, "library must be a library ID")

// parseLibraryParam reads an optional library ID. Empty is nil, not an error.
func parseLibraryParam(raw string) (*uuid.UUID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, errBadLibrary
	}
	return &id, nil
}

// resolveScope builds the view for one artist in one library (nil: the overview).
// The holdings are loaded only for a library view, and only for the albums named.
func (a *API) resolveScope(idx collection.LibraryIndex, artist models.CollectionArtist, library *uuid.UUID, rgMBIDs []string) (viewScope, error) {
	if library == nil || *library == uuid.Nil {
		scope := viewScope{followingIn: idx.FollowingIn(artist)}
		target, err := idx.WantTarget(artist, nil)
		if err == nil && collection.GovernedByLidarr(artist) {
			err = collection.ErrChooseLibrary
		}
		scope.target, scope.editable, scope.blocked = target, err == nil, blockedReason(err)
		return scope, nil
	}
	if !idx.Exists(*library) {
		return viewScope{}, failWith(http.StatusNotFound, "library not found")
	}
	scope := viewScope{library: *library, authority: idx.Authority(artist, *library), followingIn: idx.FollowingIn(artist)}
	switch scope.authority {
	case collection.AuthorityNative:
		scope.editable, scope.target = true, *library
	case collection.AuthorityLidarr:
		scope.blocked = "lidarr"
	default:
		scope.blocked = "unknown"
	}
	holdings, err := collection.LibraryHoldings(a.DB, *library, rgMBIDs)
	if err != nil {
		logger.Log.Warnf("failed to load what library %s holds: %s", *library, err.Error())
		holdings = map[string]collection.Holding{}
	}
	scope.holdings = holdings
	return scope, nil
}

// blockedReason names why an overview cannot edit, for the UI to say.
func blockedReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, collection.ErrChooseLibrary):
		return "choose_library"
	case errors.Is(err, collection.ErrNoNativeLibrary):
		return "no_native_library"
	case errors.Is(err, collection.ErrLibraryManaged):
		return "lidarr"
	default:
		return "unknown"
	}
}

// targetErr turns WantTarget's refusal into what the caller is told.
func targetErr(err error) error {
	switch {
	case errors.Is(err, collection.ErrUnknownLibrary):
		return failWith(http.StatusNotFound, "library not found")
	case errors.Is(err, collection.ErrLibraryManaged):
		return failWith(http.StatusConflict, "that library is managed by Lidarr — set what is wanted there in Lidarr, not here")
	case errors.Is(err, collection.ErrChooseLibrary):
		return failWith(http.StatusConflict, "Lidarr manages this artist and more than one library could take this — choose the library")
	case errors.Is(err, collection.ErrNoNativeLibrary):
		return failWith(http.StatusConflict, "this artist is managed by Lidarr — set what is wanted in Lidarr, not here")
	}
	logger.Log.Warnf("failed to resolve which library a want is for: %s", err.Error())
	return failWith(http.StatusInternalServerError, "failed to resolve the artist's manager")
}

// wantTarget resolves the library a want or follow for this artist is written for.
// An artist the collection does not hold yet is nobody's but Autotaggerr's, so it
// takes a want tied to no library, as it always has.
func (a *API) wantTarget(artistMBID string, library *uuid.UUID) (uuid.UUID, error) {
	var artist models.CollectionArtist
	if err := a.DB.Where("mb_id = ?", strings.TrimSpace(artistMBID)).Limit(1).Find(&artist).Error; err != nil {
		return uuid.Nil, failWith(http.StatusInternalServerError, "failed to resolve the artist's manager")
	}
	idx, err := collection.LoadLibraryIndex(a.DB)
	if err != nil {
		return uuid.Nil, failWith(http.StatusInternalServerError, "failed to load the libraries")
	}
	if artist.MBID == "" && (library == nil || *library == uuid.Nil) {
		return uuid.Nil, nil
	}
	target, err := idx.WantTarget(artist, library)
	if err != nil {
		return uuid.Nil, targetErr(err)
	}
	return target, nil
}

// artistViewIn is the artist as one view of it reads: what may be edited and
// followed there, and where an edit goes.
func artistViewIn(artist models.CollectionArtist, scope viewScope) artistView {
	view := newArtistView(artist)
	view.IdentityEditable = scope.editable
	view.EditBlocked = scope.blocked
	if scope.followingIn != nil {
		view.FollowingIn = scope.followingIn
	}
	if scope.editable && scope.target != uuid.Nil {
		target := scope.target
		view.EditLibraryID = &target
	}
	if scope.overview() {
		// Following is edited where wants are: here for an artist no manager governs,
		// in the chosen native library for one Lidarr does.
		view.FollowGoverns = scope.editable
		view.Following = artist.Monitored && collection.FollowGoverns(artist)
		return view
	}
	library := scope.library
	view.ViewLibraryID = &library
	view.FollowGoverns = scope.editable
	view.Following = scope.editable && artist.Monitored && collection.FollowAppliesIn(artist, scope.library)
	return view
}

// artistViewFor is the overview of an artist, read with the libraries it needs. A
// failed read degrades to the artist-wide rule rather than failing the response.
func (a *API) artistViewFor(artist models.CollectionArtist) artistView {
	idx, err := collection.LoadLibraryIndex(a.DB)
	if err != nil {
		logger.Log.Warnf("failed to load the libraries: %s", err.Error())
		return newArtistView(artist)
	}
	scope, _ := a.resolveScope(idx, artist, nil, nil)
	return artistViewIn(artist, scope)
}
