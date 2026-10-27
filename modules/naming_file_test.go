package modules

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// TestReadAudioProperties reads the codec and sample depth from the headers: a 16- and
// a 24-bit FLAC, and an MP3, which has a codec but no depth. A file that is not what
// its extension says is unreadable, never named for its extension.
func TestReadAudioProperties(t *testing.T) {
	requireTool(t, "ffmpeg")
	flac24 := filepath.Join(t.TempDir(), "hi.flac")
	if out, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "anullsrc=r=96000:cl=stereo",
		"-t", "0.1", "-sample_fmt", "s32", "-bits_per_raw_sample", "24", flac24).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg 24-bit flac: %v %s", err, out)
	}
	cases := []struct {
		path string
		want AudioProperties
	}{
		{synthAudio(t, ".flac"), AudioProperties{Codec: "FLAC", BitsPerSample: 16}},
		{flac24, AudioProperties{Codec: "FLAC", BitsPerSample: 24}},
		{synthAudio(t, ".mp3"), AudioProperties{Codec: "MP3"}},
	}
	for _, tc := range cases {
		got, err := ReadAudioProperties(tc.path)
		if err != nil || got != tc.want {
			t.Errorf("%s = %+v, %v; want %+v", filepath.Base(tc.path), got, err, tc.want)
		}
	}

	fake := filepath.Join(t.TempDir(), "not audio.mp3")
	if err := os.WriteFile(fake, []byte("plain text, not MPEG audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAudioProperties(fake); !errors.Is(err, ErrAudioUnreadable) {
		t.Errorf("a text file named .mp3 = %v, want ErrAudioUnreadable", err)
	}
}

// TestFileTokensRender: the three tokens read the file, and anything missing — an MP3's
// depth, a release group nobody recorded, audio that could not be read — renders empty
// and collapses its brackets, as Lidarr does.
func TestFileTokensRender(t *testing.T) {
	templates := models.NamingTemplates{TrackFile: "{track:00} {Track Title} [{MediaInfo AudioCodec}{ MediaInfo AudioBitsPerSample}]{-Release Group}"}
	render := func(file NamingFile) (string, error) {
		path, err := RenderLibraryPath(templates, sampleRumours, sampleID("32"), file)
		return path.File, err
	}

	if got, err := render(sampleFLAC); err != nil || got != "02 Dreams [FLAC 24]-GRP.flac" {
		t.Errorf("FLAC = %q, %v", got, err)
	}
	if got, err := render(sampleMP3); err != nil || got != "02 Dreams [MP3].mp3" {
		t.Errorf("MP3 without depth or group = %q, %v", got, err)
	}

	// Nothing known about the file at all: named without any of the three.
	if got, err := render(NamingFile{Ext: ".flac"}); err != nil || got != "02 Dreams.flac" {
		t.Errorf("a file nothing is known of = %q, %v; want the name without the tokens", got, err)
	}
}

// The files of one album folder can differ in codec, depth and group, so the tokens
// belong to the track file and are refused above it.
func TestFileTokensBelongToTheTrackFile(t *testing.T) {
	for _, tc := range []models.NamingTemplates{
		{AlbumFolder: "{Album Title} [{MediaInfo AudioCodec}]"},
		{AlbumFolder: "{Album Title}{ [MediaInfo AudioBitsPerSample]}"},
		{AlbumFolder: "{Album Title}-{Release Group}"},
	} {
		if check := ValidateNamingTemplates(tc); check.Errors[NamingAlbumFolder] == "" {
			t.Errorf("%q validated; a file token in the album folder must not", tc.AlbumFolder)
		}
	}
	if check := ValidateNamingTemplates(models.NamingTemplates{TrackFile: "{track:00} {Track Title} [{MediaInfo AudioCodec}]"}); !check.OK() {
		t.Errorf("codec in the track file = %+v, want valid", check.Errors)
	}
	// Made only of a token the lossy sample lacks, the track file renders empty there.
	if check := ValidateNamingTemplates(models.NamingTemplates{TrackFile: "{track}{MediaInfo AudioBitsPerSample}"}); !check.OK() {
		t.Errorf("track plus depth = %+v; {track} alone keeps it non-empty", check.Errors)
	}
}

func TestNamingUsesAudio(t *testing.T) {
	if NamingUsesAudio(models.NamingTemplates{}) {
		t.Error("the default templates read audio")
	}
	if !NamingUsesAudio(models.NamingTemplates{TrackFile: "{track:00} {Track Title} {MediaInfo AudioBitsPerSample}"}) {
		t.Error("a template with a MediaInfo token does not read audio")
	}
	if NamingUsesAudio(models.NamingTemplates{TrackFile: "{track:00} {Track Title}-{Release Group}"}) {
		t.Error("{Release Group} alone opened the file")
	}
}

func TestNamingFileFor(t *testing.T) {
	path := synthAudio(t, ".flac")
	if f := NamingFileFor(path, false, nil); f != (NamingFile{Ext: ".flac"}) {
		t.Errorf("nothing asked = %+v, want only the extension", f)
	}
	group := " GRP "
	if f := NamingFileFor(path, true, &group); f.Audio.Codec != "FLAC" || f.Audio.BitsPerSample != 16 || f.ReleaseGroup != "GRP" {
		t.Errorf("audio and group = %+v", f)
	}
}

func TestParseReleaseGroup(t *testing.T) {
	cases := map[string]string{
		"Artist-Album-WEB-FLAC-2014-GRP":             "GRP",
		"Artist.Album.2014.FLAC-GRP":                 "GRP",
		"Artist - Album (2014) [FLAC]-GRP":           "GRP",
		"Artist - Album (2014) [FLAC 24-96]":         "",
		"Artist-Album-WEB-2014":                      "", // a year is not a group
		"Artist-Album-2014-WEB":                      "", // nor a format
		"Jay-Z":                                      "", // two parts: an artist
		"Artist - Album - Remix":                     "",
		"Kate Bush - Hounds of Love (2018 Remaster)": "",
		"": "",
	}
	for name, want := range cases {
		if got := ParseReleaseGroup(name); got != want {
			t.Errorf("ParseReleaseGroup(%q) = %q, want %q", name, got, want)
		}
	}
	groups := WithDownloadReleaseGroup([]ImportGroup{{Folder: "/d/CD1"}, {Folder: "/d/Other-Album-WEB-2014-OWN", ReleaseGroup: "OWN"}}, "Artist-Discography-FLAC-GRP")
	if groups[0].ReleaseGroup != "GRP" || groups[1].ReleaseGroup != "OWN" {
		t.Errorf("download fill = %+v, want the download's group only where the folder had none", groups)
	}
}
