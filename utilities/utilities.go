package utilities

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"golang.org/x/text/unicode/norm"
)

func PrintASCII() {
	fmt.Println(``)
	fmt.Println(`A U T O T A G G E R R`)
	fmt.Println(``)
	return
}

func ValidatePasswordFormat(password string) (bool, string, error) {
	requirements := "Password must have a minimum of eight characters, at least one uppercase letter, one lowercase letter and one number."

	if len(password) < 8 {
		return false, requirements, nil
	}

	match, err := regexp.Match(`[A-ZÆØÅ]{1,20}`, []byte(password))
	if err != nil {
		return false, requirements, err
	} else if !match {
		return false, requirements, nil
	}

	match, err = regexp.Match(`[a-zæøå]{1,20}`, []byte(password))
	if err != nil {
		return false, requirements, err
	} else if !match {
		return false, requirements, nil
	}

	match, err = regexp.Match(`[0-9]{1,20}`, []byte(password))
	if err != nil {
		return false, requirements, err
	} else if !match {
		return false, requirements, nil
	}

	return true, requirements, nil
}

func FindNextSunday(poinInTime time.Time) (time.Time, error) {

	sundayDate := time.Time{}

	// Find sunday
	if poinInTime.Weekday() == 0 {
		sundayDate = poinInTime
	} else {
		nextDate := poinInTime

		for i := 0; i < 8; i++ {
			nextDate = nextDate.AddDate(0, 0, +1)
			if nextDate.Weekday() == 0 {
				sundayDate = nextDate
				break
			}
		}

	}

	if sundayDate.Weekday() == 0 {
		return SetClockToMaximum(sundayDate), nil
	}

	return time.Time{}, errors.New("Failed to find next sunday for date.")
}

func FindEarlierMonday(pointInTime time.Time) (time.Time, error) {

	mondayDate := time.Time{}

	// Find monday
	if pointInTime.Weekday() == 1 {
		mondayDate = pointInTime
	} else {
		previousDate := pointInTime

		for i := 0; i < 8; i++ {
			previousDate = previousDate.AddDate(0, 0, -1)
			if previousDate.Weekday() == 1 {
				mondayDate = previousDate
				break
			}
		}

	}

	if mondayDate.Weekday() == 1 {
		return SetClockToMinimum(mondayDate), nil
	}

	return time.Time{}, errors.New("Failed to find earlier monday for date.")
}

func FindEarlierSunday(pointInTime time.Time) (time.Time, error) {

	sundayDate := time.Time{}

	// Find monday
	if pointInTime.Weekday() == 0 {
		sundayDate = pointInTime
	} else {
		previousDate := pointInTime

		for i := 0; i < 8; i++ {
			previousDate = previousDate.AddDate(0, 0, -1)
			if previousDate.Weekday() == 0 {
				sundayDate = previousDate
				break
			}
		}

	}

	if sundayDate.Weekday() == 0 {
		return sundayDate, nil
	}

	return time.Time{}, errors.New("Failed to find earlier Sunday for date.")
}

func RemoveIntFromArray(originalArray []int, intToRemove int) []int {

	newArray := []int{}

	for _, intNumber := range originalArray {
		if intNumber != intToRemove {
			newArray = append(newArray, intNumber)
		}
	}

	return newArray

}

func SetClockToMinimum(pointInTime time.Time) (newPointInTime time.Time) {
	newPointInTime = SetClockToTime(pointInTime, 0, 0, 0, 0)
	return
}

func SetClockToMaximum(pointInTime time.Time) (newPointInTime time.Time) {
	newPointInTime = SetClockToTime(pointInTime, 23, 59, 59, 59)
	return
}

func SetClockToTime(pointInTime time.Time, hours int, minutes int, seconds int, nanoSeconds int) (newPointInTime time.Time) {
	newPointInTime = time.Date(pointInTime.Year(), pointInTime.Month(), pointInTime.Day(), hours, minutes, seconds, nanoSeconds, time.Now().Location())
	return
}

func TimeToMySQLTimestamp(pointInTime time.Time) (timeString string) {
	timeString = ""
	timeString = IntToPaddedString(pointInTime.Year()) + "-" + IntToPaddedString(int(pointInTime.Month())) + "-" + IntToPaddedString(pointInTime.Day()) + " " + IntToPaddedString(pointInTime.Hour()) + ":" + IntToPaddedString(pointInTime.Minute()) + ":" + IntToPaddedString(pointInTime.Second()) + ".000"
	return
}

func IntToPaddedString(number int) (paddedNumber string) {
	paddedNumber = ""
	if number > 9 {
		return strconv.Itoa(number)
	} else {
		paddedNumber = "0" + strconv.Itoa(number)
	}
	return
}

// Maps your "metadataType" to the canonical Vorbis key used by MusicBrainz.
func MBVorbisKeyFor(metadataType string) (string, bool) {
	switch strings.ToLower(metadataType) {
	case "track":
		return "MUSICBRAINZ_RELEASETRACKID", true
	case "release":
		return "MUSICBRAINZ_ALBUMID", true
	case "release_group":
		return "MUSICBRAINZ_RELEASEGROUPID", true
	case "recording":
		return "MUSICBRAINZ_TRACKID", true
	case "artist":
		return "MUSICBRAINZ_ALBUMARTISTID", true
	default:
		return "", false
	}
}

func NormalizeTagValue(s string) string {
	// Trim + NFC normalization avoids false mismatches (é vs. é, trailing spaces, etc.)
	return norm.NFC.String(strings.TrimSpace(s))
}

// MultiValueSeparator joins the several values one tag field can carry (genres,
// ISRCs, artist MBIDs, ...). One spelling for every such field on both engines,
// because two spellings drift apart and the writers cannot agree on a diff they
// render differently.
//
// A semicolon is the only separator that works across the players that matter:
// Plex reads tags through ffmpeg, which never gained multi-value support for
// MP4/AAC and so needs a delimited single value there; Navidrome splits genres on
// ";", "/" and "," by default. "/" and "," are unusable as separators regardless —
// "AC/DC" and "Crosby, Stills & Nash" are single names containing them.
const MultiValueSeparator = "; "

// NormalizeTagValues normalizes each value and drops the blanks. A blank would
// otherwise occupy a position in a multi-value field, or produce a dangling
// separator once joined — and a value that never matches on read-back re-tags the
// file on every scan.
//
// Values are deliberately *not* deduplicated here: whether a release that credits
// the same catalogue number under two labels should say it once or twice is a
// question about the data, not about rendering. The diff dedups both sides when it
// compares, which is where it matters.
func NormalizeTagValues(values []string) []string {
	kept := make([]string, 0, len(values))
	for _, value := range values {
		if v := NormalizeTagValue(value); v != "" {
			kept = append(kept, v)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// JoinTagValues renders several values as the one string a tag field holds.
func JoinTagValues(values []string) string {
	return strings.Join(NormalizeTagValues(values), MultiValueSeparator)
}

// DiffFlacTags compares the Vorbis comments on disk with the ones Autotaggerr wants
// and returns only the keys that need to change. Both sides are multi-valued: a
// Vorbis key may legitimately repeat, and so may the desired value once the engine
// writes the spec-correct form.
//
// The desired values must already be in the representation the writer will produce
// (see renderFLACValues) — comparing an unrendered value against what comes back off
// disk is how a file ends up re-tagged on every scan forever.
func DiffFlacTags(existing map[string][]string, desired map[string][]string, tagger models.TaggerSettings) (map[string][]string, bool) {
	changes := make(map[string][]string)
	hasChanges := false

	for k, want := range desired {
		key := strings.ToUpper(k)
		wantValues := cleanTagValues(want)

		// An empty desired value means "Autotaggerr has nothing to say about this
		// tag", not "clear it" — unless the tagger profile says otherwise.
		if len(wantValues) == 0 && !tagger.RemoveValues {
			continue
		}

		if !sameTagValues(existing[key], wantValues) {
			changes[key] = wantValues
			hasChanges = true
		}
	}
	return changes, hasChanges
}

// cleanTagValues is NormalizeTagValues plus a case-insensitive dedup that keeps the
// first spelling. It is applied to both sides of a comparison, so a file another
// tagger wrote the same genre into twice does not read back as a difference that can
// never be resolved.
func cleanTagValues(values []string) []string {
	normalized := NormalizeTagValues(values)
	if len(normalized) == 0 {
		return nil
	}
	kept := make([]string, 0, len(normalized))
	seen := make(map[string]struct{}, len(normalized))
	for _, value := range normalized {
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		kept = append(kept, value)
	}
	return kept
}

// sameTagValues reports whether what is on disk already says what we want, in the
// order we want it. want must already be cleaned.
//
// Order is part of the comparison, which it did not used to be — the values were
// sorted before being compared as one string. That was harmless while every field was
// a single joined value, but order carries meaning now: genres are ranked by community
// vote and an artist credit reads in its credited order, so a re-ordering that the
// diff could not see would never be written and the ranking would be decorative.
func sameTagValues(have, want []string) bool {
	haveValues := cleanTagValues(have)
	if len(haveValues) != len(want) {
		return false
	}
	for i := range want {
		if haveValues[i] != want[i] {
			return false
		}
	}
	return true
}

// DiffID3Tags is DiffFlacTags for ID3: same comparison, same rule about empty
// values. An empty desired value only becomes a change when the tagger profile's
// remove_values is on, so the setting means the same thing on both engines — before,
// ID3 skipped empties unconditionally and a profile that cleared a tag on FLAC
// silently left it in place on MP3.
//
// The MP3 writer must actually apply every key returned here (ffmpeg deletes a tag
// when given an empty value): its reported diff is derived from this change set, so a
// key that is reported but not written would never converge and the file would be
// rewritten on every scan.
func DiffID3Tags(existing map[string][]string, desired map[string][]string, tagger models.TaggerSettings) (map[string][]string, bool) {
	changes := make(map[string][]string)
	has := false
	for k, want := range desired {
		key := strings.ToUpper(k)
		wantValues := cleanTagValues(want)
		if len(wantValues) == 0 && !tagger.RemoveValues {
			continue
		}
		if !sameTagValues(existing[key], wantValues) {
			changes[key] = wantValues
			has = true
		}
	}
	return changes, has
}

func SplitPathIntoMediaStrings(root, trackPath string) (artist, album string, containers []string, track string, err error) {
	root = filepath.Clean(root)
	trackPath = filepath.Clean(trackPath)

	rel, err := filepath.Rel(root, trackPath)
	if err != nil {
		return "", "", nil, "", fmt.Errorf("cannot make %q relative to %q: %w", trackPath, root, err)
	}

	logger.Log.Trace("root path: " + root)
	logger.Log.Trace("track path: " + trackPath)

	// Guard against paths outside root
	if rel == "." || strings.HasPrefix(rel, "..") {
		return "", "", nil, "", fmt.Errorf("path %q is not under root %q", trackPath, root)
	}

	parts := strings.Split(rel, string(os.PathSeparator))
	if len(parts) < 3 {
		return "", "", nil, "", fmt.Errorf(
			"relative path %q too short; expected artist/album/(...)/track",
			rel,
		)
	}

	track = parts[len(parts)-1]
	if filepath.Ext(track) == "" {
		return "", "", nil, "", fmt.Errorf(
			"last segment %q is not a file (no extension) in %q",
			track, rel,
		)
	}

	artist = parts[0]
	album = parts[1]

	if len(parts) > 3 {
		containers = parts[2 : len(parts)-1]
	} else {
		containers = nil
	}

	// Normalize everything
	normalize := func(s string) string {
		return norm.NFC.String(strings.TrimSpace(s))
	}

	artist = normalize(artist)
	album = normalize(album)
	track = normalize(track)
	for i := range containers {
		containers[i] = normalize(containers[i])
	}

	return artist, album, containers, track, nil
}

// picks the artist folder name from /root/artist/album[/media]/track
func ExtractArtistNameFromTrackFilePath(root string, trackPath string) (string, error) {
	artist, _, _, _, err := SplitPathIntoMediaStrings(root, trackPath)
	if err != nil {
		return "", err
	}

	if artist == "" {
		return "", fmt.Errorf("empty artist segment in %q", trackPath)
	}
	return artist, nil
}

// picks the album folder name from /root/artist/album[/media]/track
func ExtractAlbumNameFromTrackFilePath(root string, trackPath string) (string, error) {
	_, album, _, _, err := SplitPathIntoMediaStrings(root, trackPath)
	if err != nil {
		return "", err
	}

	if album == "" {
		return "", fmt.Errorf("empty album segment in %q", trackPath)
	}
	return album, nil
}

// picks the media subdir (CD 01 / Vinyl 1 / Disc 2...) if present; returns "" if not present
func ExtractMediaNameFromTrackFilePath(root string, trackPath string) (string, error) {
	_, _, containers, _, err := SplitPathIntoMediaStrings(root, trackPath)
	if err != nil {
		return "", err
	}
	// /root/artist/album/track        -> len(parts)==3 (no media)
	// /root/artist/album/media/track  -> len(parts)>=4 (media is parts[2])
	if len(containers) == 1 {
		media := containers[0]
		if media == "" {
			return "", fmt.Errorf("empty media segment in %q", trackPath)
		}
		return media, nil
	}
	return "", nil // none or many media directories
}

// picks the track file name assuming the correct path structure
func ExtractTrackFileName(trackPath string) (string, error) {
	clean := filepath.Clean(trackPath)

	base := filepath.Base(clean)
	if base == "" || base == "." || base == string(os.PathSeparator) {
		return "", fmt.Errorf("invalid track file in %q", trackPath)
	}

	return base, nil
}

// normalize a path for matching across OSes (case-insensitive, forward slashes)
func NormPath(s string) string {
	s = filepath.Clean(s)
	s = filepath.ToSlash(s)
	return strings.ToLower(s)
}

// canonicalize for robust matching (trim, NFC, lower)
func Canon(s string) string {
	return strings.ToLower(norm.NFC.String(strings.TrimSpace(s)))
}

func BaseOfPathAny(p string) string {
	return path.Base(filepath.ToSlash(p)) // use forward slash rules
}

func BaseDirOfPathAny(p string) string {
	slashed := filepath.ToSlash(p)
	dir := path.Dir(slashed)
	return path.Base(dir)
}

func GrandfatherDirOfPathAny(p string) string {
	slashed := filepath.ToSlash(p)
	// 1) directory containing the file
	parent := path.Dir(slashed)
	// 2) directory containing the parent
	grandparent := path.Dir(parent)
	// 3) base name of that directory
	return path.Base(grandparent)
}

var (
	spaceRe = regexp.MustCompile(`\s+`)
	// Map typographic look-alikes to plain ASCII
	smartReplacer = strings.NewReplacer(
		// apostrophes / primes
		"’", "'", "‘", "'", "‛", "'", "ʻ", "'", "ʹ", "'", "ˈ", "'",
		// quotes
		"“", `"`, "”", `"`, "„", `"`, "‟", `"`,
		// dashes / hyphens / minus / non-breaking hyphen
		"–", "-", "—", "-", "−", "-", "‐", "-", "\u2011", "-",
		// ellipsis
		"…", "...",
		// weird spaces
		"\u00A0", " ", "\u2009", " ", "\u200A", " ", "\u200B", "",
	)
)

// removeDiacritics turns é → e, å → a, etc.
func removeDiacritics(s string) string {
	// NFD separates base + diacritic; then drop Mn (non-spacing mark)
	decomp := norm.NFD.String(s)
	b := make([]rune, 0, utf8.RuneCountInString(decomp))
	for _, r := range decomp {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b = append(b, r)
	}
	return string(b)
}

// stripPunct keeps letters/numbers/spaces only
func stripPunct(s string) string {
	b := make([]rune, 0, len(s))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsSpace(r) {
			b = append(b, r)
		}
	}
	return string(b)
}

// CanonLoose normalizes strings so "Schindler’s" == "Schindler's"
func CanonLoose(s string) string {
	s = strings.TrimSpace(s)
	// unify typography
	s = smartReplacer.Replace(s)
	// unify accents
	s = removeDiacritics(s)
	// optional: neutralize punctuation differences entirely
	s = stripPunct(s)
	// collapse whitespace
	s = spaceRe.ReplaceAllString(s, " ")
	// case-fold
	s = strings.ToLower(s)
	return s
}

// Equality helper
func EqLoose(a, b string) bool { return CanonLoose(a) == CanonLoose(b) }

// SortTagChanges orders a tag diff by field name, in place. Both writers build their
// diff by iterating a map, so without this the same write reports its fields in a
// different order every time — which would make an Activity detail view shuffle on
// each read and any comparison of two diffs meaningless.
func SortTagChanges(changes []models.TagChange) {
	sort.Slice(changes, func(i, j int) bool { return changes[i].Field < changes[j].Field })
}
