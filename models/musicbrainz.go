package models

type MusicBrainzData struct {
	Title       string
	Album       string
	Artist      string
	Genre       string
	Year        string
	Label       string
	ReleaseMBID string
}

type MusicBrainzReleaseResponse struct {
	CoverArtArchive struct {
		Artwork  bool `json:"artwork"`
		Front    bool `json:"front"`
		Back     bool `json:"back"`
		Darkened bool `json:"darkened"`
		Count    int  `json:"count"`
	} `json:"cover-art-archive"`
	Genres    []any `json:"genres"`
	LabelInfo []struct {
		CatalogNumber string `json:"catalog-number"`
		Label         struct {
			LabelCode      int    `json:"label-code"`
			Type           string `json:"type"`
			TypeID         string `json:"type-id"`
			Disambiguation string `json:"disambiguation"`
			SortName       string `json:"sort-name"`
			Genres         []struct {
				ID             string `json:"id"`
				Disambiguation string `json:"disambiguation"`
				Count          int    `json:"count"`
				Name           string `json:"name"`
			} `json:"genres"`
			Tags []struct {
				Name  string `json:"name"`
				Count int    `json:"count"`
			} `json:"tags"`
			Name string `json:"name"`
			ID   string `json:"id"`
		} `json:"label"`
	} `json:"label-info"`
	Disambiguation string         `json:"disambiguation"`
	Quality        string         `json:"quality"`
	Tags           []any          `json:"tags"`
	Title          string         `json:"title"`
	StatusID       string         `json:"status-id"`
	ID             string         `json:"id"`
	ArtistCredit   []ArtistCredit `json:"artist-credit"`
	ReleaseGroup   ReleaseGroup   `json:"release-group"`
	Country        string         `json:"country"`
	Asin           string         `json:"asin"`
	ReleaseEvents  []struct {
		Area struct {
			Disambiguation string   `json:"disambiguation"`
			SortName       string   `json:"sort-name"`
			Type           any      `json:"type"`
			TypeID         any      `json:"type-id"`
			ID             string   `json:"id"`
			Iso31661Codes  []string `json:"iso-3166-1-codes"`
			Name           string   `json:"name"`
		} `json:"area"`
		Date string `json:"date"`
	} `json:"release-events"`
	Packaging          any                `json:"packaging"`
	Media              []MusicBrainzMedia `json:"media"`
	Date               string             `json:"date"`
	PackagingID        any                `json:"packaging-id"`
	Status             string             `json:"status"`
	TextRepresentation struct {
		Language string `json:"language"`
		Script   string `json:"script"`
	} `json:"text-representation"`
	Barcode string `json:"barcode"`
}

type Track struct {
	Recording struct {
		Genres []struct {
			Disambiguation string `json:"disambiguation"`
			ID             string `json:"id"`
			Name           string `json:"name"`
			Count          int    `json:"count"`
		} `json:"genres"`
		ISRCs            []string       `json:"isrcs"`
		FirstReleaseDate string         `json:"first-release-date"`
		Disambiguation   string         `json:"disambiguation"`
		ArtistCredit     []ArtistCredit `json:"artist-credit"`
		Video            bool           `json:"video"`
		Length           int            `json:"length"`
		Title            string         `json:"title"`
		ID               string         `json:"id"`
		Tags             []struct {
			Name  string `json:"name"`
			Count int    `json:"count"`
		} `json:"tags"`
	} `json:"recording"`
	Number       string         `json:"number"`
	ArtistCredit []ArtistCredit `json:"artist-credit"`
	Position     int            `json:"position"`
	ID           string         `json:"id"`
	Length       int            `json:"length"`
	Title        string         `json:"title"`
}

// IsVideo reports whether this track is a video rather than a piece of audio.
//
// It is the one definition of a rule several places need: a video track is not a
// track an audio library can hold, so it must not be counted as one you are missing
// and must not be proposed as a candidate for an audio file. Frank Ocean's *Endless*
// is the case that named it — the 2018 CD+DVD edition is 19 audio tracks and 22
// videos, which a plain `len(medium.Tracks)` reports as a 41-track album that is
// permanently 22 short. An "enhanced CD" carrying one music video as its last track
// is the same bug at a smaller scale, and the reason this is a per-*track* predicate
// rather than a check on the medium's format: the medium says "CD", and only the
// recording says which of its tracks you could ever own.
//
// It reads `recording.video`, which is present on every cached release — the release
// fetch has always used `inc=recordings` — so no cache needs to be discarded for this
// to start answering correctly. A release somehow stored without recordings answers
// false for everything, which is exactly the behaviour that predates this rule.
func (t Track) IsVideo() bool { return t.Recording.Video }

type Artist struct {
	ID   string `json:"id"`
	Tags []struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	} `json:"tags"`
	Name           string `json:"name"`
	Disambiguation string `json:"disambiguation"`
	SortName       string `json:"sort-name"`
	Genres         []struct {
		Disambiguation string `json:"disambiguation"`
		ID             string `json:"id"`
		Name           string `json:"name"`
		Count          int    `json:"count"`
	} `json:"genres"`
	Type    string `json:"type"`
	Country string `json:"country"`
	TypeID  string `json:"type-id"`
}

type ArtistCredit struct {
	Joinphrase string `json:"joinphrase"`
	Artist     Artist `json:"artist"`
	Name       string `json:"name"`
}

type ReleaseGroup struct {
	SecondaryTypeIds []interface{} `json:"secondary-type-ids"`
	FirstReleaseDate string        `json:"first-release-date"`
	Disambiguation   string        `json:"disambiguation"`
	SecondaryTypes   []string      `json:"secondary-types"`
	Genres           []struct {
		Count          int    `json:"count"`
		ID             string `json:"id"`
		Name           string `json:"name"`
		Disambiguation string `json:"disambiguation"`
	} `json:"genres"`
	PrimaryType string `json:"primary-type"`
	Tags        []struct {
		Count int    `json:"count"`
		Name  string `json:"name"`
	} `json:"tags"`
	PrimaryTypeID string `json:"primary-type-id"`
	// ArtistCredit is the release-group's *own* credit, which is not the same thing
	// as any one release's. MusicBrainz credits releases and release-groups
	// independently, so during an upstream artist migration — where editors move the
	// group first — the two disagree for as long as the older pressings keep the old
	// credit. This is the authority on whose album it is; a release's credit is the
	// authority only on whose pressing that edition is.
	//
	// It arrives inside every release payload under the inc= string
	// QueryMusicBrainzReleaseData already uses, so reading it costs no extra fetch.
	ArtistCredit []ArtistCredit `json:"artist-credit"`
	Title        string         `json:"title"`
	ID           string         `json:"id"`
}

type MusicBrainzMedia struct {
	TrackOffset int     `json:"track-offset"`
	ID          string  `json:"id"`
	Position    int     `json:"position"`
	TrackCount  int     `json:"track-count"`
	Title       string  `json:"title"`
	FormatID    string  `json:"format-id"`
	Format      string  `json:"format"`
	Tracks      []Track `json:"tracks"`
}

// MusicBrainzArtistReleaseGroups is the response of the release-group browse
// endpoint (release-group?artist=<id>), used to build an artist's discography.
type MusicBrainzArtistReleaseGroups struct {
	Count         int                             `json:"release-group-count"`
	Offset        int                             `json:"release-group-offset"`
	ReleaseGroups []MusicBrainzArtistReleaseGroup `json:"release-groups"`
}

type MusicBrainzArtistReleaseGroup struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	PrimaryType      string   `json:"primary-type"`
	SecondaryTypes   []string `json:"secondary-types"`
	FirstReleaseDate string   `json:"first-release-date"`
}

// MusicBrainzArtistLookup is the /artist/<id> entity lookup: who the artist is,
// as opposed to what they released. It exists so the artist page can open with
// facts (kind, origin, active years, genres) instead of only a name — none of this
// is derivable from the files on disk, and none of it is worth persisting, since
// it is reference data one rate-limited call away.
type MusicBrainzArtistLookup struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	SortName       string `json:"sort-name"`
	Disambiguation string `json:"disambiguation"`
	// Type is "Person", "Group", "Orchestra", … — MusicBrainz's own vocabulary,
	// passed through rather than translated.
	Type    string `json:"type"`
	Gender  string `json:"gender"`
	Country string `json:"country"`
	Area    struct {
		Name string `json:"name"`
	} `json:"area"`
	BeginArea struct {
		Name string `json:"name"`
	} `json:"begin-area"`
	LifeSpan struct {
		Begin string `json:"begin"`
		End   string `json:"end"`
		Ended bool   `json:"ended"`
	} `json:"life-span"`
	Genres []MusicBrainzNamedCount `json:"genres"`
	Tags   []MusicBrainzNamedCount `json:"tags"`
}

// MusicBrainzNamedCount is a genre or tag with its community vote count, which is
// what makes it rankable — MusicBrainz returns them unsorted.
type MusicBrainzNamedCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// MusicBrainzReleaseSearchResponse is the /release?query= search result, used by
// manual attach to let a human find the right release for an unmatched file.
type MusicBrainzReleaseSearchResponse struct {
	Count    int                              `json:"count"`
	Releases []MusicBrainzReleaseSearchResult `json:"releases"`
}

// MusicBrainzReleaseSearchResult is one search hit. It is deliberately a lean
// subset: search results only need to be distinguishable from one another (which
// edition, which year, how many tracks), not complete — the full release is
// fetched once the user picks one.
type MusicBrainzReleaseSearchResult struct {
	ID             string         `json:"id"`
	Score          int            `json:"score"`
	Title          string         `json:"title"`
	Status         string         `json:"status"`
	Date           string         `json:"date"`
	Country        string         `json:"country"`
	Disambiguation string         `json:"disambiguation"`
	ArtistCredit   []ArtistCredit `json:"artist-credit"`
	ReleaseGroup   struct {
		ID             string   `json:"id"`
		Title          string   `json:"title"`
		PrimaryType    string   `json:"primary-type"`
		SecondaryTypes []string `json:"secondary-types"`
	} `json:"release-group"`
	Media []struct {
		Format     string `json:"format"`
		TrackCount int    `json:"track-count"`
	} `json:"media"`
}

// MusicBrainzArtistSearchResponse is the /artist?query= search result, used to add
// an artist you own nothing of yet.
type MusicBrainzArtistSearchResponse struct {
	Count   int                             `json:"count"`
	Artists []MusicBrainzArtistSearchResult `json:"artists"`
}

type MusicBrainzArtistSearchResult struct {
	ID             string `json:"id"`
	Score          int    `json:"score"`
	Name           string `json:"name"`
	SortName       string `json:"sort-name"`
	Disambiguation string `json:"disambiguation"`
	Type           string `json:"type"`
	Country        string `json:"country"`
}

// MusicBrainzReleaseBrowseResponse is the /release?release-group= browse result:
// every edition of one release-group. Note the count key differs from the search
// endpoint's ("release-count" vs "count") — MusicBrainz browse and search are
// different APIs that happen to return the same entity.
type MusicBrainzReleaseBrowseResponse struct {
	Count    int                              `json:"release-count"`
	Releases []MusicBrainzReleaseSearchResult `json:"releases"`
}
