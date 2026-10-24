package models

// The naming defaults reproduce the library layout the whole pipeline assumes,
// <root>/<ARTIST>/<ALBUM> (<YEAR>)/[<MEDIA FOLDER>]/<TRACKS>, so a profile nobody has
// edited proposes no moves for a library that already follows the convention.
const (
	DefaultNamingArtistFolder = "{Artist Name}"
	DefaultNamingAlbumFolder  = "{Album Title} ({Release Year})"
	DefaultNamingMediaFolder  = "CD {medium:00}"
	DefaultNamingTrackFile    = "{track:00} {Track Title}"
)

// NamingTemplates is where a file belongs under its library root, as one template
// per path level rather than one template with slashes in it. That split is the
// structural guarantee: everything that reads the layout — the collection's artist
// folders, the per-artist actions, Lidarr's folder and disc matching, folder attach —
// counts path segments, and a template that cannot contain a separator cannot change
// how many there are. See docs/naming.md.
//
// It is deliberately not part of TaggerSettings. Those feed Tagger.Fingerprint, which
// skip-unchanged compares per file, and where a file lives changes nothing about its
// tags — folding naming in would make one template edit re-tag the whole library.
type NamingTemplates struct {
	ArtistFolder string `json:"artist_folder"`
	AlbumFolder  string `json:"album_folder"`
	// MediaFolder is used only when the release has more than one medium carrying
	// audio, so a single-disc album (or a CD+DVD whose DVD is all video) stays flat.
	MediaFolder string `json:"media_folder"`
	// TrackFile is the file name without its extension; the file's own extension is
	// kept as it is.
	TrackFile string `json:"track_file"`
}

// DefaultNamingTemplates is what an unedited profile renders.
func DefaultNamingTemplates() NamingTemplates {
	return NamingTemplates{
		ArtistFolder: DefaultNamingArtistFolder,
		AlbumFolder:  DefaultNamingAlbumFolder,
		MediaFolder:  DefaultNamingMediaFolder,
		TrackFile:    DefaultNamingTrackFile,
	}
}

// WithDefaults fills every empty template with its default.
func (n NamingTemplates) WithDefaults() NamingTemplates {
	d := DefaultNamingTemplates()
	if n.ArtistFolder == "" {
		n.ArtistFolder = d.ArtistFolder
	}
	if n.AlbumFolder == "" {
		n.AlbumFolder = d.AlbumFolder
	}
	if n.MediaFolder == "" {
		n.MediaFolder = d.MediaFolder
	}
	if n.TrackFile == "" {
		n.TrackFile = d.TrackFile
	}
	return n
}

// Naming is the profile's templates with defaults filled in.
func (t TaggerProfile) Naming() NamingTemplates {
	return NamingTemplates{
		ArtistFolder: t.NamingArtistFolder,
		AlbumFolder:  t.NamingAlbumFolder,
		MediaFolder:  t.NamingMediaFolder,
		TrackFile:    t.NamingTrackFile,
	}.WithDefaults()
}

// PathDiff is where a file is against where its profile's naming would put it, both
// relative to the library root and with forward slashes.
type PathDiff struct {
	Current string `json:"current"`
	Desired string `json:"desired"`
	Changed bool   `json:"changed"`
	// Note says why a proposed move will not be acted on, when it will not.
	Note string `json:"note,omitempty"`
}
