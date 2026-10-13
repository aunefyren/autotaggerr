package modules

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// --- the reduction ----------------------------------------------------------

// grid builds beat times at a steady tempo.
func grid(bpm float64, beats int) []float64 {
	period := 60 / bpm
	out := make([]float64, beats)
	for i := range out {
		out[i] = 1.5 + float64(i)*period
	}
	return out
}

func TestTempoFromBeatsSteadyGrid(t *testing.T) {
	got, err := TempoFromBeats(grid(160, 400))
	if err != nil {
		t.Fatalf("TempoFromBeats: %v", err)
	}
	if got.BPM != 160 || got.Confidence != 1 || got.Beats != 400 {
		t.Fatalf("got %+v, want 160 BPM at confidence 1 from 400 beats", got)
	}
}

// A tracker that drops a beat leaves one interval twice as long. A mean would be
// dragged down by every one of them; the median is not.
func TestTempoFromBeatsIgnoresDroppedBeats(t *testing.T) {
	beats := grid(128, 300)
	var thinned []float64
	for i, b := range beats {
		if i%10 != 5 { // drop one beat in ten
			thinned = append(thinned, b)
		}
	}
	got, err := TempoFromBeats(thinned)
	if err != nil {
		t.Fatalf("TempoFromBeats: %v", err)
	}
	if got.BPM != 128 {
		t.Fatalf("BPM = %v, want 128 despite dropped beats", got.BPM)
	}
	if got.Confidence >= 1 || got.Confidence < 0.85 {
		t.Fatalf("confidence = %v, want slightly below 1: the long intervals disagree", got.Confidence)
	}
}

// A tracker flipping between two tempos produces a grid with no clear period. The
// confidence is what says so.
func TestTempoFromBeatsIrregularGridScoresLow(t *testing.T) {
	var beats []float64
	at := 0.0
	for i := 0; i < 200; i++ {
		if i%2 == 0 {
			at += 0.375 // 160 BPM
		} else {
			at += 0.5 // 120 BPM
		}
		beats = append(beats, at)
	}
	got, err := TempoFromBeats(beats)
	if err != nil {
		t.Fatalf("TempoFromBeats: %v", err)
	}
	if got.Confidence > 0.55 {
		t.Fatalf("confidence = %v for an alternating grid, want it low", got.Confidence)
	}
}

// Silence, a skit, an ambient intro: aubiotrack exits 0 with few or no beats. That is
// an answer, reported as ErrTooFewBeats rather than as a tempo computed from nothing.
func TestTempoFromBeatsTooFew(t *testing.T) {
	for _, beats := range [][]float64{nil, {1.0}, grid(120, minTempoBeats-1)} {
		if _, err := TempoFromBeats(beats); !errors.Is(err, ErrTooFewBeats) {
			t.Errorf("%d beats: err = %v, want ErrTooFewBeats", len(beats), err)
		}
	}
	if _, err := TempoFromBeats(grid(120, minTempoBeats)); err != nil {
		t.Errorf("%d beats should be enough: %v", minTempoBeats, err)
	}
}

func TestParseBeatTimes(t *testing.T) {
	beats, err := parseBeatTimes([]byte("1.507030\n\n1.877846\r\n2.248639\n"))
	if err != nil {
		t.Fatalf("parseBeatTimes: %v", err)
	}
	if len(beats) != 3 || beats[0] != 1.50703 || beats[2] != 2.248639 {
		t.Fatalf("got %v", beats)
	}
	if _, err := parseBeatTimes([]byte("1.5\nnot a number\n")); err == nil {
		t.Fatal("expected an error for output that is not a beat time")
	}
}

// --- the subprocess, faked --------------------------------------------------

// TestAubiotrackHelperProcess is not a test: it is the fake detector. withFakeAubiotrack
// points tempoCommand at this test binary, which re-enters here and plays back what
// the test asked for — no aubio needed, on any platform.
func TestAubiotrackHelperProcess(t *testing.T) {
	if os.Getenv("AUTOTAGGERR_FAKE_AUBIOTRACK") != "1" {
		return
	}
	fmt.Fprint(os.Stdout, os.Getenv("FAKE_STDOUT"))
	fmt.Fprint(os.Stderr, os.Getenv("FAKE_STDERR"))
	code, _ := strconv.Atoi(os.Getenv("FAKE_EXIT"))
	os.Exit(code)
}

func withFakeAubiotrack(t *testing.T, stdout, stderr string, exit int) {
	t.Helper()
	aubiotrackOnce.Do(func() {}) // the real lookup must not overwrite the fake below
	prevPath, prevAvailable, prevCommand := aubiotrackPath, aubiotrackAvailable, tempoCommand
	aubiotrackPath, aubiotrackAvailable = "aubiotrack", true
	tempoCommand = func(ctx context.Context, _ string, path string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAubiotrackHelperProcess$", "--", "-i", path)
		cmd.Env = append(os.Environ(),
			"AUTOTAGGERR_FAKE_AUBIOTRACK=1",
			"FAKE_STDOUT="+stdout,
			"FAKE_STDERR="+stderr,
			"FAKE_EXIT="+strconv.Itoa(exit),
		)
		return cmd
	}
	t.Cleanup(func() {
		aubiotrackPath, aubiotrackAvailable, tempoCommand = prevPath, prevAvailable, prevCommand
	})
}

func beatOutput(beats []float64) string {
	var b strings.Builder
	for _, beat := range beats {
		fmt.Fprintf(&b, "%.6f\n", beat)
	}
	return b.String()
}

func TestDetectTempoReadsTheDetector(t *testing.T) {
	withFakeAubiotrack(t, beatOutput(grid(174, 300)), "", 0)
	got, err := DetectTempo(context.Background(), "/music/track.flac")
	if err != nil {
		t.Fatalf("DetectTempo: %v", err)
	}
	if got.BPM != 174 || got.Beats != 300 {
		t.Fatalf("got %+v, want 174 BPM from 300 beats", got)
	}
}

// aubio prints an error per decoder it tried; the last line is its summary and the
// one a person can act on.
func TestDetectTempoReportsTheDetectorsOwnError(t *testing.T) {
	stderr := "AUBIO ERROR: source_avcodec: Failed opening /x\nAUBIO ERROR: source_sndfile: Failed opening /x\nError: could not open input file /x\n"
	withFakeAubiotrack(t, "", stderr, 1)
	_, err := DetectTempo(context.Background(), "/x")
	if err == nil || !strings.Contains(err.Error(), "could not open input file /x") {
		t.Fatalf("err = %v, want aubiotrack's own summary line", err)
	}
	if strings.Contains(err.Error(), "source_avcodec") {
		t.Fatalf("err = %v: only the summary line should be kept", err)
	}
}

// Silence is a successful run with no beats.
func TestDetectTempoSilenceIsTooFewBeats(t *testing.T) {
	withFakeAubiotrack(t, "", "", 0)
	if _, err := DetectTempo(context.Background(), "/silence.flac"); !errors.Is(err, ErrTooFewBeats) {
		t.Fatalf("err = %v, want ErrTooFewBeats", err)
	}
}

func TestDetectTempoWithoutTheBinary(t *testing.T) {
	aubiotrackOnce.Do(func() {})
	prev := aubiotrackAvailable
	aubiotrackAvailable = false
	t.Cleanup(func() { aubiotrackAvailable = prev })

	if _, err := DetectTempo(context.Background(), "/x.flac"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("err = %v, want an explanation that the detector is missing", err)
	}
}

func TestTempoDetectorVersionNamesTheRevision(t *testing.T) {
	if v := TempoDetectorVersion(); !strings.HasSuffix(v, "/r"+tempoAnalysisRevision) {
		t.Fatalf("version %q does not carry the analysis revision", v)
	}
}

// --- the audio identity -----------------------------------------------------

func writeFile(t *testing.T, name string, parts ...[]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, bytes.Join(parts, nil), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustHash(t *testing.T, path string) string {
	t.Helper()
	hash, err := AudioPayloadHash(path)
	if err != nil {
		t.Fatalf("AudioPayloadHash(%s): %v", filepath.Base(path), err)
	}
	return hash
}

// id3v2 builds an ID3v2 header claiming a tag body of the given size.
func id3v2Tag(body []byte, footer bool) []byte {
	size := len(body)
	header := []byte{'I', 'D', '3', 4, 0, 0, byte(size >> 21 & 0x7f), byte(size >> 14 & 0x7f), byte(size >> 7 & 0x7f), byte(size & 0x7f)}
	out := append(header, body...)
	if footer {
		out[5] |= 0x10
		out = append(out, []byte{'3', 'D', 'I', 4, 0, 0x10, header[6], header[7], header[8], header[9]}...)
	}
	return out
}

func id3v1Trailer() []byte {
	return append([]byte("TAG"), bytes.Repeat([]byte{'x'}, 125)...)
}

func apeFooter(items []byte, withHeader bool) []byte {
	footer := make([]byte, 32)
	copy(footer, "APETAGEX")
	binary.LittleEndian.PutUint32(footer[8:12], 2000)
	binary.LittleEndian.PutUint32(footer[12:16], uint32(len(items)+32))
	var flags uint32
	var header []byte
	if withHeader {
		flags = 1 << 31
		header = make([]byte, 32)
		copy(header, "APETAGEX")
	}
	binary.LittleEndian.PutUint32(footer[20:24], flags)
	return append(append(header, items...), footer...)
}

// flacBlock builds one metadata block header plus body.
func flacBlock(kind byte, body []byte, last bool) []byte {
	if last {
		kind |= 0x80
	}
	n := len(body)
	return append([]byte{kind, byte(n >> 16), byte(n >> 8), byte(n)}, body...)
}

var audioFrames = bytes.Repeat([]byte{0xFF, 0xFB, 0x90, 0x64}, 64)

// The same MP3 frames hash the same whatever tags surround them: a rewritten ID3v2
// tag of a different size, a footer, the ID3v1 trailer stripID3v1 removes, an APE tag.
func TestAudioPayloadHashIgnoresMP3Tags(t *testing.T) {
	bare := mustHash(t, writeFile(t, "bare.mp3", audioFrames))

	variants := map[string]string{
		"id3v2":           writeFile(t, "a.mp3", id3v2Tag(make([]byte, 100), false), audioFrames),
		"bigger id3v2":    writeFile(t, "b.mp3", id3v2Tag(make([]byte, 4096), false), audioFrames),
		"id3v2 footer":    writeFile(t, "c.mp3", id3v2Tag(make([]byte, 50), true), audioFrames),
		"id3v1":           writeFile(t, "d.mp3", audioFrames, id3v1Trailer()),
		"id3v1 + TAG+":    writeFile(t, "e.mp3", audioFrames, append([]byte("TAG+"), make([]byte, 223)...), id3v1Trailer()),
		"ape":             writeFile(t, "f.mp3", audioFrames, apeFooter([]byte("items"), false)),
		"ape with header": writeFile(t, "g.mp3", audioFrames, apeFooter([]byte("items"), true)),
		"everything at once": writeFile(t, "h.mp3", id3v2Tag(make([]byte, 300), false), audioFrames,
			apeFooter([]byte("items"), true), id3v1Trailer()),
	}
	for name, path := range variants {
		if got := mustHash(t, path); got != bare {
			t.Errorf("%s: hash differs from the bare frames", name)
		}
	}

	changed := append(append([]byte(nil), audioFrames...), 0x00)
	if mustHash(t, writeFile(t, "z.mp3", id3v2Tag(make([]byte, 100), false), changed)) == bare {
		t.Error("different audio must not hash the same")
	}
}

// metaflac writes into the metadata blocks — growing a comment block, shrinking the
// padding — and never touches the frames after them.
func TestAudioPayloadHashIgnoresFLACMetadata(t *testing.T) {
	streamInfo := flacBlock(0, make([]byte, 34), false)
	small := writeFile(t, "a.flac", []byte("fLaC"), streamInfo,
		flacBlock(4, []byte("BPM=128"), false), flacBlock(1, make([]byte, 8192), true), audioFrames)
	rewritten := writeFile(t, "b.flac", []byte("fLaC"), streamInfo,
		flacBlock(4, bytes.Repeat([]byte("ARTIST=x;"), 500), false), flacBlock(1, make([]byte, 10), true), audioFrames)
	leadingID3 := writeFile(t, "c.flac", id3v2Tag(make([]byte, 64), false), []byte("fLaC"),
		flacBlock(0, make([]byte, 34), true), audioFrames)

	want := mustHash(t, writeFile(t, "frames.bin", audioFrames))
	for _, path := range []string{small, rewritten, leadingID3} {
		if got := mustHash(t, path); got != want {
			t.Errorf("%s: hash differs from the frames alone", filepath.Base(path))
		}
	}
}

func TestAudioPayloadHashRejectsBrokenFiles(t *testing.T) {
	if _, err := AudioPayloadHash(filepath.Join(t.TempDir(), "missing.flac")); err == nil {
		t.Error("expected an error for a missing file")
	}
	truncated := writeFile(t, "t.flac", []byte("fLaC"), []byte{0x00, 0x00, 0x00, 0x22})
	if _, err := AudioPayloadHash(truncated); err == nil {
		t.Error("expected an error for metadata running past the end of the file")
	}
	onlyTag := writeFile(t, "o.mp3", id3v2Tag(make([]byte, 20), false))
	if _, err := AudioPayloadHash(onlyTag); err == nil {
		t.Error("expected an error for a file that is all tag and no audio")
	}
}

// The property the hash exists for, on real files: Autotaggerr's own tag write leaves
// the audio identity unchanged, so a written BPM tag never causes a re-analysis.
func TestAudioPayloadHashSurvivesOurOwnTagWrites(t *testing.T) {
	requireTool(t, "metaflac")
	for _, ext := range []string{".flac", ".mp3"} {
		t.Run(ext, func(t *testing.T) {
			path := synthAudio(t, ext)
			before := mustHash(t, path)
			meta := models.FileTags{
				Artist: "Bee Gees", Album: "Saturday Night Fever", Title: "Stayin’ Alive",
				Genres: []string{"disco"}, Track: "1", MBRecordingID: "rec-1",
			}
			if _, _, _, err := SetFileTags(path, meta, models.TaggerSettings{}); err != nil {
				t.Fatalf("SetFileTags: %v", err)
			}
			if after := mustHash(t, path); after != before {
				t.Fatalf("tag write changed the audio hash")
			}
		})
	}
}
