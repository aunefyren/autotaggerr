package modules

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
)

// Loudness: ReplayGain measured by rsgain (EBU R128, ReplayGain 2.0's -18 LUFS
// reference), stored per file in track_loudnesses and written into the tags by the
// tagging pass, as tempo is. See docs/replaygain.md.

// LoudnessAnalyser is the binary that measures loudness.
const LoudnessAnalyser = "rsgain"

// loudnessAnalysisRevision is bumped when how rsgain is run or read changes, so every
// stored result is redone. rsgain's own version is deliberately not part of it: a
// distro upgrade re-measuring a whole library overnight would be a surprise, and
// EBU R128 does not drift between releases.
const loudnessAnalysisRevision = "1"

// LoudnessAnalyserVersion identifies the analysis a stored result came from.
func LoudnessAnalyserVersion() string { return "custom-a/r" + loudnessAnalysisRevision }

var (
	rsgainOnce      sync.Once
	rsgainPath      string
	rsgainAvailable bool
)

// RsgainAvailable reports whether rsgain is installed. Looked up once and the absence
// logged once, like aubiotrack.
func RsgainAvailable() bool {
	rsgainOnce.Do(func() {
		path, err := exec.LookPath(LoudnessAnalyser)
		if err != nil {
			logger.Log.Info("rsgain not found on PATH; loudness (ReplayGain) analysis is unavailable " +
				"(install rsgain to enable it — the Docker image has it)")
			return
		}
		rsgainPath, rsgainAvailable = path, true
	})
	return rsgainAvailable
}

// LoudnessResult is one file's measurement, or the album's.
type LoudnessResult struct {
	Loudness float64 // integrated loudness, LUFS
	Gain     float64 // dB to the -18 LUFS reference
	Peak     float64 // linear sample peak
}

// AlbumLoudness is one album's measurement: a result per file, in the order the
// files were given, and the album's own.
type AlbumLoudness struct {
	Tracks []LoudnessResult
	Album  LoudnessResult
}

// AnalyseAlbumLoudness measures an album's files together with rsgain in album mode,
// scan only — it never lets rsgain write a tag, because a second writer the diff does
// not control is how files come to disagree with what Autotaggerr thinks they hold.
// A var so the runner's tests can measure without rsgain installed.
var AnalyseAlbumLoudness = func(ctx context.Context, paths []string) (AlbumLoudness, error) {
	if !RsgainAvailable() {
		return AlbumLoudness{}, errors.New("rsgain is not installed")
	}
	args := append([]string{"custom", "--album", "--tagmode=s", "--output", "--quiet"}, paths...)
	cmd := exec.CommandContext(ctx, rsgainPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stripANSI(stderr.String()))
		if message == "" {
			message = err.Error()
		}
		return AlbumLoudness{}, fmt.Errorf("rsgain failed: %s", message)
	}
	return parseRsgainOutput(stdout.String(), paths)
}

// parseRsgainOutput reads rsgain's tab-separated scan output: a header, a row per file
// in the order given, then an "Album" row. The rows name a file by its base name only,
// which is ambiguous across disc folders ("CD1/01.flac", "CD2/01.flac"), so they are
// matched by position and the name is checked rather than trusted.
func parseRsgainOutput(output string, paths []string) (AlbumLoudness, error) {
	var rows [][]string
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" || strings.HasPrefix(line, "Filename\t") || strings.HasPrefix(line, "sep=") {
			continue
		}
		rows = append(rows, strings.Split(line, "\t"))
	}
	if len(rows) != len(paths)+1 {
		return AlbumLoudness{}, fmt.Errorf("rsgain reported %d row(s) for %d file(s)", len(rows), len(paths))
	}
	parse := func(row []string) (LoudnessResult, error) {
		if len(row) < 4 {
			return LoudnessResult{}, fmt.Errorf("rsgain row %q is too short", strings.Join(row, "\t"))
		}
		loudness, err1 := strconv.ParseFloat(strings.TrimSpace(row[1]), 64)
		gain, err2 := strconv.ParseFloat(strings.TrimSpace(row[2]), 64)
		peak, err3 := strconv.ParseFloat(strings.TrimSpace(row[3]), 64)
		if err := errors.Join(err1, err2, err3); err != nil {
			return LoudnessResult{}, fmt.Errorf("rsgain row %q: %w", strings.Join(row, "\t"), err)
		}
		return LoudnessResult{Loudness: loudness, Gain: gain, Peak: peak}, nil
	}

	var out AlbumLoudness
	for i, path := range paths {
		if name := rows[i][0]; name != filepath.Base(path) {
			return AlbumLoudness{}, fmt.Errorf("rsgain row %d is %q, expected %q", i+1, name, filepath.Base(path))
		}
		result, err := parse(rows[i])
		if err != nil {
			return AlbumLoudness{}, err
		}
		out.Tracks = append(out.Tracks, result)
	}
	if rows[len(paths)][0] != "Album" {
		return AlbumLoudness{}, fmt.Errorf("rsgain's last row is %q, expected the album", rows[len(paths)][0])
	}
	album, err := parse(rows[len(paths)])
	if err != nil {
		return AlbumLoudness{}, err
	}
	out.Album = album
	return out, nil
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			for i += 2; i < len(s) && (s[i] < '@' || s[i] > '~'); i++ {
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ReplayGain tag keys, Picard's and rsgain's spelling on both engines (Vorbis comments;
// TXXX descriptions on MP3).
const (
	ReplayGainTrackGainKey = "REPLAYGAIN_TRACK_GAIN"
	ReplayGainTrackPeakKey = "REPLAYGAIN_TRACK_PEAK"
	ReplayGainAlbumGainKey = "REPLAYGAIN_ALBUM_GAIN"
	ReplayGainAlbumPeakKey = "REPLAYGAIN_ALBUM_PEAK"
)

var replayGainKeys = []string{ReplayGainTrackGainKey, ReplayGainTrackPeakKey, ReplayGainAlbumGainKey, ReplayGainAlbumPeakKey}

// formatReplayGain renders a stored result as rsgain writes the tags: gains to two
// decimals with " dB", peaks to six.
func formatReplayGain(row models.TrackLoudness) *models.ReplayGainTags {
	return &models.ReplayGainTags{
		TrackGain: fmt.Sprintf("%.2f dB", row.TrackGain),
		TrackPeak: fmt.Sprintf("%.6f", row.TrackPeak),
		AlbumGain: fmt.Sprintf("%.2f dB", row.AlbumGain),
		AlbumPeak: fmt.Sprintf("%.6f", row.AlbumPeak),
	}
}

// ReplayGainTagValues is the stored loudness of a file as tag values, or nil when the
// library does not write ReplayGain, nothing current is stored, or the stored result
// does not describe this audio. Like TempoTagValue it re-stamps a row whose file
// changed only in its tags, so the next run does not re-hash the file.
func ReplayGainTagValues(filePath string, tagger models.TaggerSettings) *models.ReplayGainTags {
	if tagger.ReplayGainWrite == models.BPMWriteOff || cacheDB == nil {
		return nil
	}
	var stored models.TrackLoudness
	if cacheDB.Where("path = ?", filePath).Limit(1).Find(&stored).RowsAffected == 0 {
		return nil
	}
	if stored.Error != "" || stored.Analyser != LoudnessAnalyser || stored.AnalyserVersion != LoudnessAnalyserVersion() {
		return nil
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return nil
	}
	if !(stored.Size == info.Size() && stored.ModTime != nil && stored.ModTime.Equal(info.ModTime())) {
		hash, err := AudioPayloadHash(filePath)
		if err != nil || hash == "" || hash != stored.AudioHash {
			return nil
		}
		restampLoudness(filePath, info)
	}
	return formatReplayGain(stored)
}

// RestampLoudnessAfterWrite is RestampTempoAfterWrite for the stored loudness.
func RestampLoudnessAfterWrite(filePath string) {
	if info, err := os.Stat(filePath); err == nil {
		restampLoudness(filePath, info)
	}
}

func restampLoudness(filePath string, info os.FileInfo) {
	if cacheDB == nil {
		return
	}
	if err := cacheDB.Model(&models.TrackLoudness{}).Where("path = ?", filePath).
		Updates(map[string]any{"size": info.Size(), "mod_time": info.ModTime()}).Error; err != nil {
		logger.Log.Debugf("could not re-stamp the stored loudness of %q: %s", filePath, err.Error())
	}
}

// addReplayGain puts the four tags into a desired map, only when there are values:
// an absent key is one the diff never looks at, so a library that does not write
// ReplayGain cannot have remove_values clear another tool's.
func addReplayGain(desired map[string][]string, values *models.ReplayGainTags) {
	if values == nil {
		return
	}
	desired[ReplayGainTrackGainKey] = single(values.TrackGain)
	desired[ReplayGainTrackPeakKey] = single(values.TrackPeak)
	desired[ReplayGainAlbumGainKey] = single(values.AlbumGain)
	desired[ReplayGainAlbumPeakKey] = single(values.AlbumPeak)
}

// applyReplayGainPolicy is the fill-empty half of the library's ReplayGain setting:
// a file that already carries a track gain — from foobar2000, an earlier rsgain run,
// a store — keeps all four values as they are, so a track gain is never paired with
// another tool's album gain. Overwrite replaces them.
func applyReplayGainPolicy(desired, existing map[string][]string, tagger models.TaggerSettings) {
	if _, wanted := desired[ReplayGainTrackGainKey]; !wanted || tagger.ReplayGainWrite == models.BPMWriteOverwrite {
		return
	}
	for _, value := range existing[ReplayGainTrackGainKey] {
		if strings.TrimSpace(value) != "" {
			for _, key := range replayGainKeys {
				delete(desired, key)
			}
			return
		}
	}
}
