package modules

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// alienRelease is the production case: two discs that both open with a "Main Title"
// of the same recording, 4:12 on disc 1 and 3:34 on disc 2.
func alienRelease() models.MusicBrainzReleaseResponse {
	one := models.Track{ID: "alien-1-1", Position: 1, Title: "Main Title", Length: 252000}
	one.Recording.ID = "rec-main-title"
	two := models.Track{ID: "alien-2-1", Position: 1, Title: "Main Title", Length: 214000}
	two.Recording.ID = "rec-main-title"
	return models.MusicBrainzReleaseResponse{
		ID: "rel-alien",
		Media: []models.MusicBrainzMedia{
			{Position: 1, Tracks: []models.Track{one}},
			{Position: 2, Tracks: []models.Track{two}},
		},
	}
}

func withDuration(t *testing.T, seconds float64, known bool) {
	t.Helper()
	orig := audioDuration
	audioDuration = func(string) (float64, bool) { return seconds, known }
	t.Cleanup(func() { audioDuration = orig })
}

// In a flat folder the folder says nothing, so the file's own length decides between
// look-alikes — relatively, by which one is clearly closer.
func TestVerifyDiscFolderByDuration(t *testing.T) {
	release := alienRelease()
	discOne, discTwo := release.Media[0], release.Media[1]
	flat := "/music/Jerry Goldsmith/Alien (1979)/01 Main Title.flac"
	lidarr := models.Correlation{Source: models.CorrelationSourceLidarr}

	cases := []struct {
		name        string
		seconds     float64
		known       bool
		track       models.Track
		media       models.MusicBrainzMedia
		correlation models.Correlation
		wantRefusal bool
	}{
		// 4:19 on disk, as measured: 7s from disc 1's 4:12, 45s from disc 2's 3:34.
		{"resolved to the far look-alike", 259, true, discTwo.Tracks[0], discTwo, lidarr, true},
		{"resolved to the near one", 259, true, discOne.Tracks[0], discOne, lidarr, false},
		// Both within ordinary drift of the file: neither is evidence.
		{"both close", 233, true, discTwo.Tracks[0], discTwo, lidarr, false},
		{"duration unknown", 0, false, discTwo.Tracks[0], discTwo, lidarr, false},
		{"a manual correlation outranks it", 259, true, discTwo.Tracks[0], discTwo,
			models.Correlation{Source: models.CorrelationSourceManual}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withDuration(t, tc.seconds, tc.known)
			err := verifyDiscFolder(flat, tc.correlation, tc.track, tc.media, release)
			if tc.wantRefusal != errors.Is(err, ErrDiscMismatch) {
				t.Fatalf("err = %v, want refusal %t", err, tc.wantRefusal)
			}
		})
	}

	// No look-alike — the other disc's track 1 is a different song — means nothing to
	// compare, however far the length is.
	different := alienRelease()
	different.Media[1].Tracks[0].Title = "The Landing"
	different.Media[1].Tracks[0].Recording.ID = "rec-landing"
	withDuration(t, 400, true)
	if err := verifyDiscFolder(flat, lidarr, different.Media[0].Tracks[0], different.Media[0], different); err != nil {
		t.Errorf("no look-alike: err = %v, want none", err)
	}

	// A folder that names a disc keeps deciding by itself; the duration is not asked.
	withDuration(t, 259, true)
	named := "/music/Jerry Goldsmith/Alien (1979)/CD 02/01 Main Title.flac"
	if err := verifyDiscFolder(named, lidarr, discTwo.Tracks[0], discTwo, release); err != nil {
		t.Errorf("folder names the resolved disc: err = %v, want none", err)
	}
}

// mp3Frames builds n MPEG-1 Layer III frames at 128 kbit/s, 44.1 kHz: 417 bytes each
// (no padding), 1152 samples each.
func mp3Frames(n int) []byte {
	frame := make([]byte, 417)
	frame[0], frame[1], frame[2], frame[3] = 0xFF, 0xFB, 0x90, 0x00
	out := make([]byte, 0, n*len(frame))
	for i := 0; i < n; i++ {
		out = append(out, frame...)
	}
	return out
}

func TestMP3DurationCountsFrames(t *testing.T) {
	dir := t.TempDir()
	want := 100 * 1152.0 / 44100

	// An ID3v2 tag in front, junk before the first sync, and an ID3v1 tag behind.
	id3 := append([]byte("ID3\x04\x00\x00\x00\x00\x00\x0a"), make([]byte, 10)...)
	body := append(append(id3, []byte{0x00, 0x00, 0x00}...), mp3Frames(100)...)
	body = append(body, append([]byte("TAG"), make([]byte, 125)...)...)
	path := filepath.Join(dir, "a.mp3")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := mp3Duration(path)
	if !ok || got < want-0.001 || got > want+0.001 {
		t.Fatalf("duration = %v (%t), want %.3f", got, ok, want)
	}

	notAudio := filepath.Join(dir, "b.mp3")
	if err := os.WriteFile(notAudio, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := mp3Duration(notAudio); ok {
		t.Error("a file with no frames reported a duration")
	}
}

func TestFLACDurationFromStreamInfo(t *testing.T) {
	path := synthAudio(t, ".flac") // 0.1 s of silence
	got, ok := flacDuration(path)
	if !ok || got < 0.09 || got > 0.11 {
		t.Fatalf("duration = %v (%t), want 0.1", got, ok)
	}
}
