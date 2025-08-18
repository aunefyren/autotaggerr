package models

// Minimal identity payload from Plex /identity
type PlexIdentity struct {
	MachineIdentifier string `xml:"machineIdentifier,attr"`
	Version           string `xml:"version,attr"`
	FriendlyName      string `xml:"friendlyName,attr"`
}

type PlexMediaContainer struct {
	Directory []PlexDirectory `xml:"Directory"` // albums & artists
	Track     []PlexTrack     `xml:"Track"`     // tracks when type=10 searches
}

type PlexDirectory struct {
	Key         string `xml:"key,attr"`
	Title       string `xml:"title,attr"`
	Type        string `xml:"type,attr"`        // "artist", "album"
	ParentTitle string `xml:"parentTitle,attr"` // album->artist
	Year        int    `xml:"year,attr"`
}

type PlexTrack struct {
	Key              string `xml:"key,attr"`              // track key
	Title            string `xml:"title,attr"`            // track title
	ParentTitle      string `xml:"parentTitle,attr"`      // album title
	GrandparentTitle string `xml:"grandparentTitle,attr"` // artist name
	ParentKey        string `xml:"parentKey,attr"`        // album path e.g. /library/metadata/12345
	ParentRatingKey  string `xml:"parentRatingKey,attr"`  // album numeric key
	Year             int    `xml:"year,attr"`
}
