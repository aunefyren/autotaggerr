package models

import "testing"

// TestDataSourceLabel: prose names each source the way it spells itself, and a type
// this build does not know is named badly rather than blanked.
func TestDataSourceLabel(t *testing.T) {
	cases := map[string]string{
		DataSourceTypeMusicBrainz:     "MusicBrainz",
		DataSourceTypeAcoustID:        "AcoustID",
		DataSourceTypeCoverArtArchive: "Cover Art Archive",
		DataSourceTypeFanart:          "fanart.tv",
		"discogs":                     "discogs",
	}
	for sourceType, want := range cases {
		if got := DataSourceLabel(sourceType); got != want {
			t.Errorf("DataSourceLabel(%q) = %q, want %q", sourceType, got, want)
		}
	}
}

// TestMigrationSourceDefaultsToMusicBrainz: rows written before the source column
// existed are MusicBrainz's.
func TestMigrationSourceDefaultsToMusicBrainz(t *testing.T) {
	if got := (MusicbrainzMigration{}).SourceLabel(); got != "MusicBrainz" {
		t.Errorf("legacy row label = %q, want MusicBrainz", got)
	}
	if got := (MusicbrainzMigration{Source: DataSourceTypeFanart}).SourceType(); got != DataSourceTypeFanart {
		t.Errorf("SourceType = %q, want the stored one", got)
	}
}

// TestTaggerProfileSettingsProjectsEveryField: a field left out of the projection
// would silently never reach the tag writers.
func TestTaggerProfileSettingsProjectsEveryField(t *testing.T) {
	p := TaggerProfile{
		RemoveValues: true, UseCurrentArtistName: true, UseCustomArtistDelimiter: true,
		CustomArtistDelimiter: " / ", CustomArtistDelimiterCommas: true,
		IgnoreRedundantContributingArtists: true, MaxGenres: 3, MP3MultiValueTags: true,
	}
	want := TaggerSettings{
		RemoveValues: true, UseCurrentArtistName: true, UseCustomArtistDelimiter: true,
		CustomArtistDelimiter: " / ", CustomArtistDelimiterCommas: true,
		IgnoreRedundantContributingArtists: true, MaxGenres: 3, MP3MultiValueTags: true,
	}
	if got := p.Settings(); got != want {
		t.Errorf("Settings() = %+v, want %+v", got, want)
	}
}

func TestDesireDerived(t *testing.T) {
	for source, want := range map[string]bool{
		DesireSourceManual: false, "": false, DesireSourceAuto: true, DesireSourceManager: true,
	} {
		if got := (CollectionDesire{Source: source}).Derived(); got != want {
			t.Errorf("Derived() for %q = %v, want %v", source, got, want)
		}
	}
}

func TestTrackIsVideo(t *testing.T) {
	var track Track
	if track.IsVideo() {
		t.Error("a track without recording data is not a video")
	}
	track.Recording.Video = true
	if !track.IsVideo() {
		t.Error("a video recording should be reported as video")
	}
}
