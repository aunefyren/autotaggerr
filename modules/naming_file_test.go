package modules

import (
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/quality"
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
		{synthAudio(t, ".flac"), AudioProperties{Codec: "FLAC", BitsPerSample: 16, SampleRate: 44100, VBRQuality: -1}},
		{flac24, AudioProperties{Codec: "FLAC", BitsPerSample: 24, SampleRate: 96000, VBRQuality: -1}},
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

// synthMP3 encodes two seconds of tone with libmp3lame and the given rate options.
func synthMP3(t *testing.T, args ...string) string {
	t.Helper()
	requireTool(t, "ffmpeg")
	path := filepath.Join(t.TempDir(), "track.mp3")
	cmd := append([]string{"-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100",
		"-ac", "2", "-t", "2", "-c:a", "libmp3lame"}, args...)
	if out, err := exec.Command("ffmpeg", append(cmd, path)...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg mp3 %v: %v %s", args, err, out)
	}
	return path
}

// TestReadMP3Quality: CBR reads its bitrate from the frame header; VBR is known from
// the Xing header, with its average bitrate from the totals there. ffmpeg's muxer
// leaves the Xing quality field at zero, so these files say nothing of their preset —
// TestMP3XingPreset covers reading one.
func TestReadMP3Quality(t *testing.T) {
	cases := []struct {
		args    []string
		vbr     bool
		kbps    int // CBR only
		quality quality.ID
	}{
		{[]string{"-b:a", "320k"}, false, 320, quality.MP3320},
		{[]string{"-b:a", "128k"}, false, 128, quality.MP3128},
		{[]string{"-q:a", "0"}, true, 0, ""},
		{[]string{"-q:a", "2"}, true, 0, ""},
	}
	for _, tc := range cases {
		got, err := ReadAudioProperties(synthMP3(t, tc.args...))
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if got.Codec != "MP3" || got.SampleRate != 44100 || got.VBR != tc.vbr || got.BitsPerSample != 0 {
			t.Errorf("%v = %+v", tc.args, got)
		}
		if !tc.vbr && (got.BitrateKbps != tc.kbps || got.Quality() != tc.quality) {
			t.Errorf("%v = %d kbit/s, %s; want %d, %s", tc.args, got.BitrateKbps, got.Quality(), tc.kbps, tc.quality)
		}
		if tc.vbr && (got.BitrateKbps <= 0 || got.VBRQuality != -1) {
			t.Errorf("%v: VBR average bitrate not read, or a preset invented: %+v", tc.args, got)
		}
	}
}

// TestMP3XingPreset: LAME writes 100 - 10*V - q into the Xing quality field, so the
// preset is its tens digit whatever algorithm quality q was used.
func TestMP3XingPreset(t *testing.T) {
	frame := make([]byte, 512)
	copy(frame, []byte{0xFF, 0xFB, 0x90, 0x00}) // MPEG-1 Layer III, stereo: side info 32
	x := 4 + 32
	copy(frame[x:], "Xing")
	binary.BigEndian.PutUint32(frame[x+4:], 0xF)
	binary.BigEndian.PutUint32(frame[x+8:], 6000)       // frames
	binary.BigEndian.PutUint32(frame[x+12:], 4_800_000) // bytes
	for _, tc := range []struct {
		field uint32
		want  quality.ID
	}{{97, quality.MP3VBRV0}, {78, quality.MP3VBRV2}, {100, quality.MP3VBRV0}} {
		binary.BigEndian.PutUint32(frame[x+16+100:], tc.field)
		got := mp3FrameProperties(frame, 1152, 44100)
		if !got.VBR || got.Quality() != tc.want || got.BitrateKbps != 245 {
			t.Errorf("quality field %d = %+v, %s; want %s", tc.field, got, got.Quality(), tc.want)
		}
	}
}

// TestMP3FrameWithoutXing: a plain CBR frame, and one carrying Fraunhofer's VBRI header.
func TestMP3FrameWithoutXing(t *testing.T) {
	frame := make([]byte, 128)
	copy(frame, []byte{0xFF, 0xFB, 0x90, 0x00}) // MPEG-1 Layer III, 128 kbit/s, 44.1 kHz, stereo
	if got := mp3FrameProperties(frame, 1152, 44100); got.VBR || got.BitrateKbps != 128 || got.Quality() != quality.MP3128 {
		t.Errorf("plain CBR frame = %+v", got)
	}
	copy(frame[36:], "VBRI")
	binary.BigEndian.PutUint32(frame[36+10:], 5_000_000) // bytes
	binary.BigEndian.PutUint32(frame[36+14:], 6000)      // frames: 156.7 s
	got := mp3FrameProperties(frame, 1152, 44100)
	if !got.VBR || got.BitrateKbps != 255 || got.Quality() != quality.MP3VBRV0 {
		t.Errorf("VBRI frame = %+v, quality %s", got, got.Quality())
	}
}
