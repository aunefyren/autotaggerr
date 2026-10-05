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
	base := models.TaggerSettings{
		UseCurrentArtistName:        true,
		UseCustomArtistDelimiter:    true,
		CustomArtistDelimiter:       " & ",
		CustomArtistDelimiterCommas: true,
	}

	tests := []struct {
		name    string
		artists []models.ArtistCredit
		mutate  func(c *models.TaggerSettings)
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
			mutate: func(c *models.TaggerSettings) { c.UseCustomArtistDelimiter = false },
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

func TestParseMusicBrainzDate(t *testing.T) {
	tests := []struct {
		in       string
		wantYear string
		wantDate string
		wantErr  bool
	}{
		{in: "2020-05-01", wantYear: "2020", wantDate: "2020-05-01"},
		// Partial dates are the regression: MusicBrainz returns these routinely, and
		// they used to fail to parse, leaving the file with no date or year at all.
		{in: "1983-10", wantYear: "1983", wantDate: "1983-10"},
		{in: "1983", wantYear: "1983", wantDate: "1983"},
		{in: " 1983 ", wantYear: "1983", wantDate: "1983"},
		{in: "", wantErr: true},
		{in: "not-a-date", wantErr: true},
		{in: "1983-13", wantErr: true},
		{in: "1983-02-30", wantErr: true},
		{in: "83", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			year, date, err := ParseMusicBrainzDate(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseMusicBrainzDate(%q) = (%q, %q), want error", tt.in, year, date)
				}
				if year != "" || date != "" {
					t.Errorf("ParseMusicBrainzDate(%q) returned (%q, %q) alongside an error, want empty", tt.in, year, date)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseMusicBrainzDate(%q) unexpected error: %v", tt.in, err)
			}
			if year != tt.wantYear || date != tt.wantDate {
				t.Errorf("ParseMusicBrainzDate(%q) = (%q, %q), want (%q, %q)", tt.in, year, date, tt.wantYear, tt.wantDate)
			}
		})
	}
}
