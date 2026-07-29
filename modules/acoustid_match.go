package modules

import (
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// AcoustIDConfidenceFloor is the score below which a candidate is not offered at
// all. Fingerprint matching fails closed: a file left unmatched costs the user one
// manual attach, while a confident wrong match writes the wrong album into their
// tags and then self-heals into looking correct forever after.
const AcoustIDConfidenceFloor = 0.55

// folderWeight caps how far folder agreement can move a candidate within the
// headroom above its fingerprint score. It orders suggestions; it never promotes
// one to a certainty.
const folderWeight = 0.6

// MatchHint is what the file itself says about where it lives. AcoustID identifies
// a *recording*, which typically appears on a dozen releases (album, single,
// compilation, remaster); the folder is usually the only evidence of which one this
// file is, so it is the tie-breaker rather than an afterthought.
type MatchHint struct {
	Album  string
	Artist string
	Year   int
	// Tracks is how many audio files sit in the same folder, when known. An album
	// folder with 12 files matching a 12-track release is strong evidence; matching
	// a 40-track compilation is not.
	Tracks int
}

// RankedCandidate is one suggestion plus why it ranked where it did.
type RankedCandidate struct {
	AcoustIDCandidate
	// Confidence blends AcoustID's fingerprint score with how well the release
	// agrees with the folder. It is not a probability — it is an ordering, exposed
	// so the UI can show *why* one suggestion beat another.
	Confidence float64  `json:"confidence"`
	Reasons    []string `json:"reasons"`
}

// yearPattern reads a year out of a folder name like "Rumours (1977)".
var yearPattern = regexp.MustCompile(`\((\d{4})\)`)

// HintFromPath reads artist, album and year out of the library layout
// `<root>/<ARTIST>/<ALBUM> (<YEAR>)/[<MEDIA>]/<TRACK>`.
func HintFromPath(path string) MatchHint {
	parts := strings.FieldsFunc(strings.ReplaceAll(path, `\`, "/"), func(r rune) bool { return r == '/' })
	if len(parts) < 2 {
		return MatchHint{}
	}
	albumIndex := len(parts) - 2
	if isMediaFolder(parts[albumIndex]) && albumIndex > 0 {
		albumIndex--
	}

	folder := parts[albumIndex]
	hint := MatchHint{Album: strings.TrimSpace(yearPattern.ReplaceAllString(folder, ""))}
	if match := yearPattern.FindStringSubmatch(folder); match != nil {
		hint.Year, _ = strconv.Atoi(match[1])
	}
	if albumIndex > 0 {
		hint.Artist = parts[albumIndex-1]
	}
	return hint
}

func isMediaFolder(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, prefix := range []string{"cd", "disc", "disk"} {
		if rest, ok := strings.CutPrefix(lower, prefix); ok {
			if _, err := strconv.Atoi(strings.TrimSpace(rest)); err == nil {
				return true
			}
		}
	}
	return false
}

// PickAcoustIDMatch ranks candidates against what the file's folder says, drops
// everything below the confidence floor, and collapses duplicates.
//
// Pure and table-tested on purpose: this is the function that decides which album
// a fingerprint means, and getting it wrong is the failure that matters. It only
// ever *ranks* — the caller offers the result as a suggestion for a human to
// confirm, never as an automatic correlation.
func PickAcoustIDMatch(candidates []AcoustIDCandidate, hint MatchHint) []RankedCandidate {
	seen := map[string]bool{}
	ranked := make([]RankedCandidate, 0, len(candidates))

	for _, candidate := range candidates {
		key := candidate.RecordingMBID + "|" + candidate.ReleaseMBID
		if seen[key] {
			continue
		}
		seen[key] = true

		// The floor is applied to the *fingerprint* score, not to the blended
		// confidence. Folder evidence says which release a recording is from; it is
		// no evidence at all that the audio matches, so it must never lift a weak
		// match over the bar — that is precisely the confident-but-wrong answer this
		// whole design refuses to give.
		if candidate.Score < AcoustIDConfidenceFloor {
			continue
		}

		confidence, reasons := scoreCandidate(candidate, hint)
		ranked = append(ranked, RankedCandidate{
			AcoustIDCandidate: candidate,
			Confidence:        confidence,
			Reasons:           reasons,
		})
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Confidence != ranked[j].Confidence {
			return ranked[i].Confidence > ranked[j].Confidence
		}
		// Deterministic tie-break, so the same input never reorders between calls.
		return ranked[i].ReleaseMBID < ranked[j].ReleaseMBID
	})
	return ranked
}

// scoreCandidate blends the fingerprint score with agreement between the release
// and the folder. The fingerprint score dominates — it is the only evidence about
// the *audio* — and the folder evidence adjusts within the remaining headroom, so
// a strong album match can never rescue a weak fingerprint.
func scoreCandidate(c AcoustIDCandidate, hint MatchHint) (float64, []string) {
	confidence := c.Score
	var reasons []string

	if c.Score >= 0.9 {
		reasons = append(reasons, "strong audio match")
	} else if c.Score >= 0.7 {
		reasons = append(reasons, "good audio match")
	}

	headroom := 1 - c.Score
	bonus := 0.0

	if hint.Album != "" && c.ReleaseTitle != "" {
		switch similarity := titleSimilarity(hint.Album, c.ReleaseTitle); {
		case similarity >= 0.95:
			bonus += 0.55
			reasons = append(reasons, "album folder matches the release title")
		case similarity >= 0.6:
			bonus += 0.3
			reasons = append(reasons, "album folder is close to the release title")
		default:
			// A release that disagrees with the folder is not disqualified — the
			// folder may just be named differently — but it must not outrank one
			// that agrees.
			bonus -= 0.25
		}
	}

	if hint.Year > 0 && c.ReleaseYear > 0 {
		switch diff := abs(hint.Year - c.ReleaseYear); {
		case diff == 0:
			bonus += 0.3
			reasons = append(reasons, "year matches")
		case diff <= 1:
			bonus += 0.1
		default:
			bonus -= 0.1
		}
	}

	if hint.Tracks > 0 && c.TrackCount > 0 {
		if hint.Tracks == c.TrackCount {
			bonus += 0.25
			reasons = append(reasons, "track count matches the folder")
		} else if abs(hint.Tracks-c.TrackCount) > 3 {
			bonus -= 0.15
		}
	}

	if c.ReleaseMBID == "" {
		// Identifies the song but not an album: still useful (the user can pick the
		// release), but never the top suggestion when a full match exists.
		bonus -= 0.2
		reasons = append(reasons, "no release — song only")
	}

	// Folder evidence adjusts within a fraction of the remaining headroom, so it
	// orders candidates without ever letting a middling fingerprint present itself
	// as a certainty.
	confidence += clamp(bonus, -1, 1) * headroom * folderWeight
	return clamp(confidence, 0, 1), reasons
}

// titleSimilarity compares two titles ignoring case, punctuation and the noise
// that distinguishes an edition from its album ("(Deluxe Edition)", "[Remastered]").
// Returns 1 for an exact match after normalisation, otherwise the fraction of
// shared words.
func titleSimilarity(a, b string) float64 {
	left, right := normalizeTitle(a), normalizeTitle(b)
	if left == "" || right == "" {
		return 0
	}
	if left == right {
		return 1
	}

	leftWords := strings.Fields(left)
	rightWords := map[string]int{}
	for _, w := range strings.Fields(right) {
		rightWords[w]++
	}
	shared := 0
	for _, w := range leftWords {
		if rightWords[w] > 0 {
			rightWords[w]--
			shared++
		}
	}
	longer := len(leftWords)
	if n := len(strings.Fields(right)); n > longer {
		longer = n
	}
	if longer == 0 {
		return 0
	}
	return float64(shared) / float64(longer)
}

// editionNoise is the parenthetical/bracketed matter that names an edition rather
// than the album — dropped before comparing, since the folder rarely carries it.
var editionNoise = regexp.MustCompile(`[\(\[][^\)\]]*[\)\]]`)

func normalizeTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = editionNoise.ReplaceAllString(s, " ")
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) {
			return r
		}
		return ' '
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func clamp(v, low, high float64) float64 {
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}

// AudioFilesInFolder counts the audio files sitting beside a path, which is the
// track-count hint. Errors are not worth reporting: a missing count simply drops
// that signal from the score.
func AudioFilesInFolder(path string) int {
	entries, err := filepath.Glob(filepath.Join(filepath.Dir(path), "*"))
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		switch strings.ToLower(filepath.Ext(entry)) {
		case ".flac", ".mp3", ".m4a", ".ogg", ".wav", ".opus", ".aac", ".wma":
			count++
		}
	}
	return count
}
