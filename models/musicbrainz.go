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
	ArtistCredit  []struct {
		Name   string `json:"name"`
		Artist struct {
			Name           string `json:"name"`
			SortName       string `json:"sort-name"`
			ID             string `json:"id"`
			TypeID         string `json:"type-id"`
			Country        string `json:"country"`
			Disambiguation string `json:"disambiguation"`
			Type           string `json:"type"`
		} `json:"artist"`
		Joinphrase string `json:"joinphrase"`
	} `json:"artist-credit"`
	Title string `json:"title"`
	ID    string `json:"id"`
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
