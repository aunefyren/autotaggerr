package modules

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// rsgainOutput is rsgain 3.5.3's real scan output for two files and their album,
// captured with `rsgain custom --album --tagmode=s --output --quiet`.
const rsgainOutput = "Filename\tLoudness (LUFS)\tGain (dB)\tPeak\t Peak (dB)\tPeak Type\tClipping Adjustment?\n" +
	"01.flac\t-27.75\t9.75\t0.062653\t-24.06\tSample\tN\n" +
	"01.mp3\t-22.05\t4.05\t0.119049\t-18.49\tSample\tN\n" +
	"Album\t-24.02\t6.02\t0.119049\t-18.49\tSample\tN\n"

func TestParseRsgainOutput(t *testing.T) {
	paths := []string{"/music/A/CD1/01.flac", "/music/A/CD2/01.mp3"}
	got, err := parseRsgainOutput(rsgainOutput, paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tracks) != 2 || got.Tracks[0].Gain != 9.75 || got.Tracks[1].Peak != 0.119049 || got.Tracks[0].Loudness != -27.75 {
		t.Errorf("tracks = %+v", got.Tracks)
	}
	if got.Album.Gain != 6.02 || got.Album.Peak != 0.119049 {
		t.Errorf("album = %+v", got.Album)
	}

	// Rows are matched by position, and the name checked: a row out of order is an
	// error, never another file's gain.
	if _, err := parseRsgainOutput(rsgainOutput, []string{paths[1], paths[0]}); err == nil {
		t.Error("rows in the wrong order were accepted")
	}
	if _, err := parseRsgainOutput(rsgainOutput, paths[:1]); err == nil {
		t.Error("a row count that does not match the files was accepted")
	}
	noAlbum := strings.Replace(rsgainOutput, "Album\t", "Other\t", 1)
	if _, err := parseRsgainOutput(noAlbum, paths); err == nil {
		t.Error("output without an album row was accepted")
	}
}

func TestStripANSI(t *testing.T) {
	if got := stripANSI("[\x1b[1;31m✘\x1b[0m] File list is not valid"); got != "[✘] File list is not valid" {
		t.Errorf("stripANSI = %q", got)
	}
}

func storeLoudness(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := AudioPayloadHash(path)
	if err != nil {
		t.Fatal(err)
	}
	mod := info.ModTime()
	row := models.TrackLoudness{
		Path: path, Size: info.Size(), ModTime: &mod, AudioHash: hash,
		TrackGain: -6.5, TrackPeak: 0.987654321, AlbumGain: -7, AlbumPeak: 1,
		Analyser: LoudnessAnalyser, AnalyserVersion: LoudnessAnalyserVersion(), AnalysedAt: time.Now(),
	}
	if err := cacheDB.Save(&row).Error; err != nil {
		t.Fatal(err)
	}
}

// TestReplayGainTagValues: rsgain's formatting, only when the library writes it, and
// only while the stored result describes this audio.
func TestReplayGainTagValues(t *testing.T) {
	dbForCache(t)
	path := synthAudio(t, ".flac")
	storeLoudness(t, path)
	write := models.TaggerSettings{ReplayGainWrite: models.BPMWriteOverwrite}

	got := ReplayGainTagValues(path, write)
	if got == nil || got.TrackGain != "-6.50 dB" || got.TrackPeak != "0.987654" || got.AlbumGain != "-7.00 dB" || got.AlbumPeak != "1.000000" {
		t.Fatalf("values = %+v", got)
	}
	if ReplayGainTagValues(path, models.TaggerSettings{}) != nil {
		t.Error("values for a library that does not write ReplayGain")
	}
	cacheDB.Model(&models.TrackLoudness{}).Where("path = ?", path).Update("error", "rsgain failed")
	if ReplayGainTagValues(path, write) != nil {
		t.Error("values from a failed analysis")
	}
}

// TestReplayGainReachesTheFileOnBothEngines: the four tags written to FLAC and MP3 under
// Picard's and rsgain's names, converging on a second write; fill_empty leaves a file
// that already has a track gain exactly as it is, overwrite replaces it.
func TestReplayGainReachesTheFileOnBothEngines(t *testing.T) {
	values := &models.ReplayGainTags{TrackGain: "-6.50 dB", TrackPeak: "0.987654", AlbumGain: "-7.00 dB", AlbumPeak: "1.000000"}
	other := &models.ReplayGainTags{TrackGain: "-1.00 dB", TrackPeak: "0.100000", AlbumGain: "-2.00 dB", AlbumPeak: "0.200000"}

	for _, ext := range []string{".flac", ".mp3"} {
		if ext == ".flac" {
			requireTool(t, "metaflac")
		}
		path := synthAudio(t, ext)
		meta := fullFileTags()
		meta.ReplayGain = values
		overwrite := models.TaggerSettings{ReplayGainWrite: models.BPMWriteOverwrite}
		if _, _, _, err := SetFileTags(path, meta, overwrite); err != nil {
			t.Fatal(err)
		}
		tags, err := ReadFileTagMap(path)
		if err != nil {
			t.Fatal(err)
		}
		for key, want := range map[string]string{
			ReplayGainTrackGainKey: "-6.50 dB", ReplayGainTrackPeakKey: "0.987654",
			ReplayGainAlbumGainKey: "-7.00 dB", ReplayGainAlbumPeakKey: "1.000000",
		} {
			if got := firstTag(tags, key); got != want {
				t.Errorf("%s %s = %q, want %q", ext, key, got, want)
			}
		}
		if unchanged, _, _, err := SetFileTags(path, meta, overwrite); err != nil || !unchanged {
			t.Errorf("%s second write changed something: %v", ext, err)
		}

		// fill_empty: the file has a track gain now, so new values are not written.
		meta.ReplayGain = other
		if unchanged, _, _, err := SetFileTags(path, meta, models.TaggerSettings{ReplayGainWrite: models.BPMWriteFillEmpty}); err != nil || !unchanged {
			t.Errorf("%s fill_empty over existing ReplayGain wrote: %v", ext, err)
		}
		// overwrite replaces them.
		if _, _, _, err := SetFileTags(path, meta, overwrite); err != nil {
			t.Fatal(err)
		}
		tags, _ = ReadFileTagMap(path)
		if got := firstTag(tags, ReplayGainAlbumGainKey); got != "-2.00 dB" {
			t.Errorf("%s after overwrite album gain = %q", ext, got)
		}
	}
}

// TestAnalyseAlbumLoudnessWithRsgain runs the real rsgain on two synthesized files of
// different loudness, in two folders with one file name, and checks the reading: a
// result per file in order, the quieter file needing more gain, an album in between.
// Skipped where rsgain is not installed (CI installs it).
func TestAnalyseAlbumLoudnessWithRsgain(t *testing.T) {
	requireTool(t, "rsgain")
	requireTool(t, "ffmpeg")
	dir := t.TempDir()
	loud, quiet := dir+"/CD1/01.flac", dir+"/CD2/01.flac"
	for path, volume := range map[string]string{loud: "volume=0dB", quiet: "volume=-12dB"} {
		if err := os.MkdirAll(path[:strings.LastIndex(path, "/")], 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi",
			"-i", "sine=frequency=440:duration=3", "-af", volume, path).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v %s", err, out)
		}
	}
	got, err := AnalyseAlbumLoudness(context.Background(), []string{loud, quiet})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tracks) != 2 || got.Tracks[1].Gain <= got.Tracks[0].Gain {
		t.Errorf("tracks = %+v, want the quieter second file to need more gain", got.Tracks)
	}
	if got.Album.Gain < got.Tracks[0].Gain || got.Album.Gain > got.Tracks[1].Gain {
		t.Errorf("album gain %v is not between the tracks' %v and %v", got.Album.Gain, got.Tracks[0].Gain, got.Tracks[1].Gain)
	}
	// Scan only: rsgain wrote nothing into the files.
	tags, err := ReadFileTagMap(loud)
	if err != nil {
		t.Fatal(err)
	}
	if firstTag(tags, ReplayGainTrackGainKey) != "" {
		t.Error("rsgain wrote a tag; it must only scan")
	}
}
