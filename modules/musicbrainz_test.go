package modules

import (
	"io"
	"testing"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/sirupsen/logrus"
)

// The functions under test log through the package-level logger.Log, which is
// nil until InitLogger runs. Point it at a discarding logger so tests exercise
// the logic without touching the filesystem.
func init() {
	logger.Log = logrus.New()
	logger.Log.SetOutput(io.Discard)
}

func credit(name, joinphrase string) models.ArtistCredit {
	return models.ArtistCredit{
		Name:       name,
		Joinphrase: joinphrase,
		Artist:     models.Artist{Name: name},
	}
}

func TestMusicBrainzArtistsArrayToString(t *testing.T) {
	base := models.ConfigStruct{
		AutotaggerrUseCurrentArtistName:        true,
		AutotaggerrUseCustomArtistDelimiter:    true,
		AutotaggerrCustomArtistDelimiter:       " & ",
		AutotaggerrCustomArtistDelimiterCommas: true,
	}

	tests := []struct {
		name    string
		artists []models.ArtistCredit
		mutate  func(c *models.ConfigStruct)
		want    string
	}{
		{
			name:    "single artist",
			artists: []models.ArtistCredit{credit("Artist A", "")},
			want:    "Artist A",
		},
		{
			name:    "two artists use custom delimiter",
			artists: []models.ArtistCredit{credit("Artist A", " feat. "), credit("Artist B", "")},
			want:    "Artist A & Artist B",
		},
		{
			name: "three artists use commas then delimiter",
			artists: []models.ArtistCredit{
				credit("Artist A", " feat. "),
				credit("Artist B", " & "),
				credit("Artist C", ""),
			},
			want: "Artist A, Artist B & Artist C",
		},
		{
			name: "custom delimiter disabled falls back to join phrase",
			artists: []models.ArtistCredit{
				credit("Artist A", " feat. "),
				credit("Artist B", ""),
			},
			mutate: func(c *models.ConfigStruct) { c.AutotaggerrUseCustomArtistDelimiter = false },
			want:   "Artist A feat. Artist B",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base
			if tt.mutate != nil {
				tt.mutate(&cfg)
			}
			got := MusicBrainzArtistsArrayToString(tt.artists, cfg)
			if got != tt.want {
				t.Errorf("MusicBrainzArtistsArrayToString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMusicBrainzDateStringToDateTime(t *testing.T) {
	parsed, err := MusicBrainzDateStringToDateTime("2020-05-01")
	if err != nil {
		t.Fatalf("unexpected error parsing valid date: %v", err)
	}
	if parsed.Year() != 2020 || parsed.Month() != 5 || parsed.Day() != 1 {
		t.Errorf("parsed date = %v, want 2020-05-01", parsed)
	}

	if _, err := MusicBrainzDateStringToDateTime("not-a-date"); err == nil {
		t.Error("expected error for invalid date string, got nil")
	}
}
