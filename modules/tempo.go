// Tempo (BPM) detection: run aubio's beat tracker over a file and reduce the beats it
// reports to one tempo and a confidence.
//
// Like fingerprinting, the feature is detachable, and either switch being off leaves
// Autotaggerr behaving exactly as it did before it existed:
//
//  1. `aubiotrack` on PATH — missing means unavailable, logged once, never an error;
//  2. per-library opt-in (Library.AnalyseTempo) — off by default.
//
// aubiotrack rather than `aubio tempo`: the latter is aubio's Python front end, which
// would bring Python and numpy into the image, and prints only a *mean* of the
// per-beat tempos with no confidence at all. The C tool prints the beat times, so the
// reduction is ours to choose — a median, which one dropped or doubled beat cannot
// drag, and a confidence read off the same intervals.
//
// See docs/tempo.md for how good the numbers are in practice.
package modules

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
)

const (
	// TempoDetector names the binary in every stored result.
	TempoDetector = "aubiotrack"

	// tempoAnalysisRevision versions what this file does with the beats. Bump it when
	// the reduction changes (the median, the confidence window, the beat floor) and
	// every stored result is redone on the next pass. aubiotrack has no version flag,
	// so an aubio upgrade on its own does not invalidate anything — deliberately, since
	// a distro bump re-analysing a whole library overnight would be a surprise.
	tempoAnalysisRevision = "1"

	// minTempoBeats is the fewest beats a tempo is estimated from. Below it the
	// interval set is too small for a median to mean anything — a spoken intro, a
	// skit, an ambient piece, or silence (which aubiotrack answers with no beats and
	// a successful exit).
	minTempoBeats = 16

	// tempoConfidenceWindow is how close to the median an interval must be to count
	// as agreeing with it. ±5% is about ±8 BPM at 160: wide enough for a human drummer,
	// narrow enough that a tracker flipping between two tempos scores low.
	tempoConfidenceWindow = 0.05

	// tempoTimeout bounds one analysis. aubiotrack runs at several hundred times real
	// time, so this is a guard against a hung decoder, not a budget for long files.
	tempoTimeout = 5 * time.Minute
)

// ErrTooFewBeats is an analysis that ran fine and found nothing to measure. It is an
// answer about the audio, not a failure, and is reported apart from real errors.
var ErrTooFewBeats = errors.New("too few beats to estimate a tempo")

var (
	aubiotrackOnce      sync.Once
	aubiotrackPath      string
	aubiotrackAvailable bool

	// tempoCommand builds the detector subprocess. A variable so a test can run a
	// fake detector without the real binary installed.
	tempoCommand = func(ctx context.Context, binary, path string) *exec.Cmd {
		return exec.CommandContext(ctx, binary, "-i", path)
	}
)

// AubiotrackAvailable reports whether the tempo detector is installed. Looked up once
// and the absence logged once — an unavailable optional feature is a fact about the
// deployment, not a per-file error.
func AubiotrackAvailable() bool {
	aubiotrackOnce.Do(func() {
		path, err := exec.LookPath(TempoDetector)
		if err != nil {
			logger.Log.Info("aubiotrack not found on PATH; tempo analysis is unavailable " +
				"(install aubio — aubio-tools on Debian/Ubuntu — to enable it)")
			return
		}
		aubiotrackPath, aubiotrackAvailable = path, true
	})
	return aubiotrackAvailable
}

// TempoDetectorVersion identifies the analysis a stored result came from: the
// detector's method (aubiotrack's defaults) and this file's reduction of its output.
func TempoDetectorVersion() string {
	return "default/r" + tempoAnalysisRevision
}

// TempoAnalysis is one file's tempo.
type TempoAnalysis struct {
	BPM        float64
	Confidence float64
	Beats      int
}

// DetectTempo runs the beat tracker over a file. It decodes the whole file, so callers
// go through the stored result (the tempo package) rather than calling this per scan.
// Cancelling ctx kills the subprocess.
func DetectTempo(ctx context.Context, path string) (TempoAnalysis, error) {
	if !AubiotrackAvailable() {
		return TempoAnalysis{}, errors.New("aubiotrack is not installed, so tempo cannot be analysed")
	}

	ctx, cancel := context.WithTimeout(ctx, tempoTimeout)
	defer cancel()

	cmd := tempoCommand(ctx, aubiotrackPath, path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return TempoAnalysis{}, fmt.Errorf("tempo analysis of %s timed out", path)
		}
		if ctx.Err() != nil {
			return TempoAnalysis{}, ctx.Err()
		}
		// aubio prints one "AUBIO ERROR" line per decoder it tried, then a summary;
		// the summary is the last line and the one worth keeping.
		if message := lastLine(stderr.String()); message != "" {
			return TempoAnalysis{}, fmt.Errorf("aubiotrack failed: %s", message)
		}
		return TempoAnalysis{}, fmt.Errorf("aubiotrack failed: %w", err)
	}

	beats, err := parseBeatTimes(out)
	if err != nil {
		return TempoAnalysis{}, err
	}
	return TempoFromBeats(beats)
}

// parseBeatTimes reads aubiotrack's output: one beat time in seconds per line.
func parseBeatTimes(out []byte) ([]float64, error) {
	var beats []float64
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		value, err := strconv.ParseFloat(line, 64)
		if err != nil {
			return nil, fmt.Errorf("could not read aubiotrack output %q", line)
		}
		beats = append(beats, value)
	}
	return beats, scanner.Err()
}

// TempoFromBeats reduces beat times to a tempo and a confidence.
//
// The tempo is 60 over the *median* inter-beat interval. A beat tracker that drops a
// beat produces one interval twice as long, and a mean would let a handful of those
// pull the whole figure down; the median ignores them.
//
// The confidence is the share of intervals within tempoConfidenceWindow of that
// median: how regular the beat grid was. It is a statement about the grid, not about
// whether the grid is the right one — a tracker locked steadily onto double time
// scores as high as one locked onto the true tempo. That is why half/double-tempo is
// left to whoever uses the number (docs/tempo.md).
func TempoFromBeats(beats []float64) (TempoAnalysis, error) {
	intervals := make([]float64, 0, len(beats))
	for i := 1; i < len(beats); i++ {
		if d := beats[i] - beats[i-1]; d > 0 {
			intervals = append(intervals, d)
		}
	}
	if len(intervals)+1 < minTempoBeats {
		return TempoAnalysis{Beats: len(beats)}, fmt.Errorf("%w (%d found)", ErrTooFewBeats, len(beats))
	}

	sorted := append([]float64(nil), intervals...)
	sort.Float64s(sorted)
	n := len(sorted)
	median := sorted[n/2]
	if n%2 == 0 {
		median = (sorted[n/2-1] + sorted[n/2]) / 2
	}

	agreeing := 0
	for _, d := range intervals {
		if math.Abs(d-median) <= median*tempoConfidenceWindow {
			agreeing++
		}
	}

	return TempoAnalysis{
		BPM:        roundTo(60/median, 2),
		Confidence: roundTo(float64(agreeing)/float64(len(intervals)), 3),
		Beats:      len(beats),
	}, nil
}

func roundTo(value float64, places int) float64 {
	scale := math.Pow(10, float64(places))
	return math.Round(value*scale) / scale
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// AudioPayloadHash hashes a file's audio with every tag block left out: a leading
// ID3v2 tag, FLAC's metadata blocks, and a trailing ID3v1 / APEv2 tag.
//
// It is the identity a stored tempo is checked against when a file's size or mtime
// has moved. Writing tags changes both — and the BPM tag itself is a tag write — so a
// size/mtime key alone would re-analyse every file once for every write, ours
// included. The audio frames are untouched by a tag write, so this hash is not.
//
// It reads the whole file but decodes nothing, which is why it is only computed once
// the cheap size/mtime check has already failed.
func AudioPayloadHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	start, err := audioPayloadStart(file)
	if err != nil {
		return "", err
	}
	end, err := audioPayloadEnd(file, info.Size())
	if err != nil {
		return "", err
	}
	if end <= start {
		return "", fmt.Errorf("%s has no audio between its tags", path)
	}

	hasher := sha256.New()
	if _, err := io.Copy(hasher, io.NewSectionReader(file, start, end-start)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// audioPayloadStart skips a leading ID3v2 tag and, for FLAC, the metadata blocks —
// where metaflac writes, and whose padding absorbs most edits.
func audioPayloadStart(file io.ReaderAt) (int64, error) {
	var start int64
	header := make([]byte, 10)
	if _, err := file.ReadAt(header, 0); err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	if string(header[:3]) == "ID3" {
		// A syncsafe size: four 7-bit bytes, excluding the 10-byte header and the
		// optional 10-byte footer.
		size := int64(header[6])<<21 | int64(header[7])<<14 | int64(header[8])<<7 | int64(header[9])
		start = 10 + size
		if header[5]&0x10 != 0 {
			start += 10
		}
	}

	marker := make([]byte, 4)
	if _, err := file.ReadAt(marker, start); err != nil {
		if errors.Is(err, io.EOF) {
			return start, nil
		}
		return 0, err
	}
	if string(marker) != "fLaC" {
		return start, nil
	}

	position := start + 4
	block := make([]byte, 4)
	for {
		if _, err := file.ReadAt(block, position); err != nil {
			return 0, fmt.Errorf("truncated FLAC metadata: %w", err)
		}
		length := int64(block[1])<<16 | int64(block[2])<<8 | int64(block[3])
		position += 4 + length
		if block[0]&0x80 != 0 {
			return position, nil
		}
	}
}

// audioPayloadEnd trims a trailing ID3v1 tag (and the "TAG+" block in front of it)
// and an APEv2 tag. stripID3v1 removes ID3v1 on our own MP3 writes, so a file going
// from "has one" to "has none" must hash the same.
func audioPayloadEnd(file io.ReaderAt, size int64) (int64, error) {
	end := size
	read := func(offset, length int64) ([]byte, bool) {
		if offset < 0 {
			return nil, false
		}
		buf := make([]byte, length)
		if _, err := file.ReadAt(buf, offset); err != nil {
			return nil, false
		}
		return buf, true
	}

	if tag, ok := read(end-128, 3); ok && string(tag) == "TAG" {
		end -= 128
		if extended, ok := read(end-227, 4); ok && string(extended) == "TAG+" {
			end -= 227
		}
	}

	if footer, ok := read(end-32, 32); ok && string(footer[:8]) == "APETAGEX" {
		// The size counts the items and the footer; a header, when flagged, is 32
		// bytes more in front of them.
		tagSize := int64(binary.LittleEndian.Uint32(footer[12:16]))
		flags := binary.LittleEndian.Uint32(footer[20:24])
		if flags&(1<<31) != 0 {
			tagSize += 32
		}
		if tagSize > 0 && tagSize <= end {
			end -= tagSize
		}
	}
	return end, nil
}

// TempoTagKey is the tag the tempo is written to, under the same key on both engines:
// the Vorbis comment `BPM` on FLAC (the de facto spelling — Picard's, Mixxx's), and
// the standard TBPM frame on MP3 through id3TextFrameForKey.
const TempoTagKey = "BPM"

// TempoTagValue is the BPM a file should carry under a profile, or "" for "nothing to
// say": the profile does not write tempo, the file has no stored result, the result
// is below the profile's confidence floor, or it no longer describes the file's audio.
//
// The value is a whole number. That is what every consumer reads (TBPM is defined as
// an integer), and a stored 104.96 written as "105" round-trips exactly, so the diff
// converges on the first write.
//
// A stored row whose size or mtime no longer match the file is checked against the
// audio hash before it is trusted. A mismatch there almost always means tags were
// written since the last analysis — the audio is the same, and the value stands — but
// a replaced or re-encoded file would otherwise be tagged with the old file's tempo
// until the next pass caught up.
func TempoTagValue(filePath string, tagger models.TaggerSettings) string {
	if tagger.BPMWrite == models.BPMWriteOff || cacheDB == nil {
		return ""
	}
	var stored models.TrackTempo
	if cacheDB.Where("path = ?", filePath).Limit(1).Find(&stored).RowsAffected == 0 {
		return ""
	}
	floor := tagger.BPMMinConfidence
	if floor <= 0 {
		floor = models.DefaultBPMMinConfidence
	}
	if stored.BPM <= 0 || stored.Confidence < floor {
		return ""
	}

	info, err := os.Stat(filePath)
	if err != nil {
		return ""
	}
	sameStamp := stored.Size == info.Size() && stored.ModTime != nil && stored.ModTime.Equal(info.ModTime())
	if !sameStamp {
		hash, err := AudioPayloadHash(filePath)
		if err != nil || hash == "" || hash != stored.AudioHash {
			return ""
		}
	}
	return strconv.Itoa(int(math.Round(stored.BPM)))
}

// applyTempoPolicy is the fill-empty half of the profile's BPM setting, applied once
// the file's current tags are known. In fill-empty mode an existing BPM — another
// tagger's, a DJ tool's, a hand-entered one — is left exactly as it is, by taking the
// key out of what is wanted. "0" counts as empty: several tools write it to mean
// "not analysed".
//
// Removing the key, rather than wanting the existing value, is also what keeps
// remove_values away from it — and the builders only add the key when there is a
// value to write, for the same reason: an empty BPM must never become a request to
// clear the tag.
func applyTempoPolicy(desired, existing map[string][]string, tagger models.TaggerSettings) {
	if _, wanted := desired[TempoTagKey]; !wanted {
		return
	}
	if tagger.BPMWrite != models.BPMWriteOverwrite && hasTempo(existing[TempoTagKey]) {
		delete(desired, TempoTagKey)
	}
}

func hasTempo(values []string) bool {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" && value != "0" {
			return true
		}
	}
	return false
}
