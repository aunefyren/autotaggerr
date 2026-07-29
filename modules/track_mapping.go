package modules

import (
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// How a file was paired with a track. Surfaced to the UI so the mandatory review
// step can say *why* it proposed a pairing — "by number" is trustworthy at a
// glance, "by order" needs looking at.
const (
	MapByNumber = "number" // the filename's track number matched a track's position
	MapByOrder  = "order"  // no usable numbers; files zipped against the tracklist
	MapUnmapped = ""       // nothing proposed; the user must pick or skip
)

// FileTrackMapping is one proposed file → track pairing.
type FileTrackMapping struct {
	Path        string `json:"path"`
	TrackID     string `json:"track_id"`
	RecordingID string `json:"recording_id"`
	TrackNumber string `json:"track_number"`
	TrackTitle  string `json:"track_title"`
	Medium      int    `json:"medium"`
	How         string `json:"how"`
}

// trackNumberPattern matches a leading "5", "05", "1-05" or "1.05" on a filename,
// optionally followed by a separator. The disc group is only meaningful when the
// two-part form is used; a bare "05" leaves it empty.
var trackNumberPattern = regexp.MustCompile(`^(?:(\d{1,2})[-_.])?(\d{1,3})(?:[^0-9]|$)`)

// discFolderPattern picks a disc number out of a folder name ("CD2", "Disc 3").
var discFolderPattern = regexp.MustCompile(`(?i)\b(?:cd|disc|disk)\s*[-_]?\s*(\d{1,2})\b`)

// MapFilesToTracks proposes a pairing of files to a release's tracks.
//
// This is the heart of bulk attach and deliberately a pure function: it is the one
// place where a wrong answer mistags an entire album in a single click, so it is
// table-tested rather than discovered in production. It only ever *proposes* —
// every pairing goes through a human review step before anything is written, and
// each file is still validated against the real release when it is attached.
//
// Strategy, in order:
//  1. Every file yields a track number, and each (disc, number) resolves to exactly
//     one track → map by number. Disc comes from a "1-05" filename or a CD2-style
//     parent folder; on a single-medium release it is not needed at all.
//  2. Otherwise fall back to sort order: paths sorted, zipped against the flattened
//     tracklist. Files past the end of the tracklist stay unmapped.
//
// Paths are not required to be sorted on the way in; the order of the returned
// slice matches the order of the input.
func MapFilesToTracks(paths []string, tracks []ReleaseTrack) []FileTrackMapping {
	out := make([]FileTrackMapping, len(paths))
	for i, path := range paths {
		out[i] = FileTrackMapping{Path: path, How: MapUnmapped}
	}
	if len(paths) == 0 || len(tracks) == 0 {
		return out
	}

	if byNumber := mapByNumber(paths, tracks); byNumber != nil {
		for i, track := range byNumber {
			if track != nil {
				out[i] = mappingFor(paths[i], *track, MapByNumber)
			}
		}
		return out
	}

	// Sort-order fallback. Sorting a copy keeps the caller's order intact, which is
	// what the review table renders.
	order := make([]int, len(paths))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return strings.ToLower(paths[order[a]]) < strings.ToLower(paths[order[b]])
	})
	for position, index := range order {
		if position >= len(tracks) {
			break
		}
		out[index] = mappingFor(paths[index], tracks[position], MapByOrder)
	}
	return out
}

// mapByNumber returns a per-file track pointer, or nil if the number strategy does
// not apply cleanly. It is all-or-nothing on purpose: a partial number match means
// the filenames are not the reliable signal they looked like, and silently mixing
// two strategies within one album is how a plausible-looking wrong mapping happens.
func mapByNumber(paths []string, tracks []ReleaseTrack) []*ReleaseTrack {
	multiMedium := false
	for _, track := range tracks {
		if track.Medium != tracks[0].Medium {
			multiMedium = true
			break
		}
	}

	result := make([]*ReleaseTrack, len(paths))
	seen := map[string]bool{}
	for i, path := range paths {
		disc, number, ok := parseTrackNumber(path)
		if !ok {
			return nil
		}
		// On a single-medium release the disc number is noise — a file called
		// "1-05" on a single CD still means track 5.
		if !multiMedium {
			disc = 0
		}

		var match *ReleaseTrack
		for j := range tracks {
			if tracks[j].Position != number {
				continue
			}
			if disc > 0 && tracks[j].Medium != disc {
				continue
			}
			if match != nil {
				return nil // ambiguous: two discs both have this track number
			}
			match = &tracks[j]
		}
		if match == nil {
			return nil
		}
		if seen[match.TrackID] {
			return nil // two files claim the same track
		}
		seen[match.TrackID] = true
		result[i] = match
	}
	return result
}

// parseTrackNumber reads a (disc, track) pair out of a path. The disc number comes
// from a "2-05" filename or, failing that, a "CD2"/"Disc 2" parent folder.
func parseTrackNumber(path string) (disc int, track int, ok bool) {
	base := filepath.Base(filepath.FromSlash(strings.ReplaceAll(path, `\`, "/")))
	base = strings.TrimSuffix(base, filepath.Ext(base))

	match := trackNumberPattern.FindStringSubmatch(strings.TrimSpace(base))
	if match == nil {
		return 0, 0, false
	}
	track, err := strconv.Atoi(match[2])
	if err != nil || track <= 0 {
		return 0, 0, false
	}
	if match[1] != "" {
		disc, _ = strconv.Atoi(match[1])
	}
	if disc == 0 {
		disc = discFromFolder(path)
	}
	return disc, track, true
}

// discFromFolder reads a disc number from the file's own folder only. Walking
// further up would misread a library root like "/music/CD Rips".
func discFromFolder(path string) int {
	normalized := strings.ReplaceAll(path, `\`, "/")
	folder := filepath.Base(filepath.Dir(normalized))
	if match := discFolderPattern.FindStringSubmatch(folder); match != nil {
		disc, _ := strconv.Atoi(match[1])
		return disc
	}
	return 0
}

func mappingFor(path string, track ReleaseTrack, how string) FileTrackMapping {
	return FileTrackMapping{
		Path:        path,
		TrackID:     track.TrackID,
		RecordingID: track.RecordingID,
		TrackNumber: track.Number,
		TrackTitle:  track.Title,
		Medium:      track.Medium,
		How:         how,
	}
}
