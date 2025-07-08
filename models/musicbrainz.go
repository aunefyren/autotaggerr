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
	Packaging any `json:"packaging"`
	Media     []struct {
		TrackOffset int    `json:"track-offset"`
		ID          string `json:"id"`
		Position    int    `json:"position"`
		TrackCount  int    `json:"track-count"`
		Title       string `json:"title"`
		FormatID    string `json:"format-id"`
		Format      string `json:"format"`
		Tracks      []struct {
			Recording struct {
				Genres []struct {
					Disambiguation string `json:"disambiguation"`
					ID             string `json:"id"`
					Name           string `json:"name"`
					Count          int    `json:"count"`
				} `json:"genres"`
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
		} `json:"tracks"`
	} `json:"media"`
	Date               string `json:"date"`
	PackagingID        any    `json:"packaging-id"`
	Status             string `json:"status"`
	TextRepresentation struct {
		Language string `json:"language"`
		Script   string `json:"script"`
	} `json:"text-representation"`
	Barcode string `json:"barcode"`
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
