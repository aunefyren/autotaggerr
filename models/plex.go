package models

type PlexSection struct {
	Key      string `json:"key"`
	Type     string `json:"type"` // "artist" for music sections
	Location []struct {
		Path string `json:"path"`
	} `json:"Location"`
}

type PlexSections struct {
	MediaContainer struct {
		Directory []PlexSection `json:"Directory"`
	} `json:"MediaContainer"`
}
