package main

import (
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/modules"
)

// TestAudioFiles: generated files read back through the app's own readers — format,
// duration source, MusicBrainz IDs — and the audio hash follows the seed, not the tags.
func TestAudioFiles(t *testing.T) {
	dir := t.TempDir()
	tags := FileTags{Title: "Song", Artist: "A", Album: "Album", AlbumArtist: "A", Track: 3, Disc: 1, Date: "1999",
		ReleaseID: "rel", ReleaseGroupID: "rg", ReleaseTrackID: "trk", RecordingID: "rec", AlbumArtistID: "art"}
	for _, format := range []string{"flac", "mp3"} {
		a := filepath.Join(dir, "a."+format)
		b := filepath.Join(dir, "b."+format)
		c := filepath.Join(dir, "c."+format)
		other := tags
		other.Title = "Retagged"
		if err := writeAudio(a, format, tags, 200000, 42); err != nil {
			t.Fatal(err)
		}
		if err := writeAudio(b, format, other, 200000, 42); err != nil {
			t.Fatal(err)
		}
		if err := writeAudio(c, format, tags, 200000, 43); err != nil {
			t.Fatal(err)
		}
		props, err := modules.ReadAudioProperties(a)
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if (format == "flac") != (props.Codec == "FLAC") {
			t.Errorf("%s read as %s", format, props.Codec)
		}
		for kind, want := range map[string]string{"release": "rel", "track": "trk", "recording": "rec"} {
			var got string
			switch kind {
			case "release":
				got, err = modules.ExtractMusicBrainzReleaseID(a)
			case "track":
				got, err = modules.ExtractMusicBrainzTrackID(a)
			default:
				got, err = modules.ExtractMusicBrainzRecordingID(a)
			}
			if err != nil || got != want {
				t.Errorf("%s %s = %q, %v; want %q", format, kind, got, err, want)
			}
		}
		ha, _ := modules.AudioPayloadHash(a)
		hb, _ := modules.AudioPayloadHash(b)
		hc, _ := modules.AudioPayloadHash(c)
		if ha == "" || ha != hb {
			t.Errorf("%s: same seed, different tags hash %q vs %q", format, ha, hb)
		}
		if ha == hc {
			t.Errorf("%s: different seeds share a hash", format)
		}
	}
}
