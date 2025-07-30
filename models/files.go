package models

type FileTags struct {
	Artist      string `json:"artist"`
	AlbumArtist string `json:"album_artist"`
	Genre       string `json:"genre"`
	Date        string `json:"date"`
	Year        string `json:"year"`
}
