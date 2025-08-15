package models

type PlexMediaContainer struct {
	Directory []PlexDirectory `xml:"Directory"`
}

type PlexDirectory struct {
	Key         string `xml:"key,attr"`
	Title       string `xml:"title,attr"`
	Type        string `xml:"type,attr"`        // "artist", "album"
	ParentTitle string `xml:"parentTitle,attr"` // album->artist
	Year        int    `xml:"year,attr"`
}
