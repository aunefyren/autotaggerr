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

	"github.com/aunefyren/autotaggerr/logger"
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

// DiffFlacTags compares existing Vorbis tags (multi-valued) with desired (single-valued per key).
// It returns only the keys that need to change.
func DiffFlacTags(existing map[string][]string, desired map[string]string) (map[string]string, bool) {
	changes := make(map[string]string)
	hasChanges := false

	for k, want := range desired {
		if strings.TrimSpace(want) == "" {
			continue
		}
		key := strings.ToUpper(k)

		wantNorm := NormalizeTagValue(want)
		haveNorm := canonicalizeValues(existing[key]) // handles []string

		// Compare canonicalized strings
		if wantNorm != haveNorm {
			changes[key] = want
			hasChanges = true
		}
	}
	return changes, hasChanges
}

// canonicalizeValues normalizes, dedups, sorts, and then joins values so comparison is stable.
// This also makes comparison order-insensitive for multi-valued tags (e.g., multiple ARTIST entries).
func canonicalizeValues(vals []string) string {
	if len(vals) == 0 {
		return ""
	}
	tmp := make([]string, 0, len(vals))
	seen := make(map[string]struct{})
	for _, v := range vals {
		n := NormalizeTagValue(v)
		if n == "" {
			continue
		}
		// case-insensitive dedup
		key := strings.ToLower(n)
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			tmp = append(tmp, n)
		}
	}
	if len(tmp) == 0 {
		return ""
	}
	sort.Strings(tmp)
	// Use a separator that won't appear in tags; only for comparison
	return strings.Join(tmp, "\x1f")
}

func DiffID3Tags(existing map[string][]string, desired map[string]string) (map[string]string, bool) {
	changes := make(map[string]string)
	has := false
	for k, want := range desired {
		if strings.TrimSpace(want) == "" {
			continue
		}
		wantN := NormalizeTagValue(want)
		haveN := canonicalizeValues(existing[strings.ToUpper(k)])
		if wantN != haveN {
			changes[strings.ToUpper(k)] = want
			has = true
		}
	}
	return changes, has
}

// internal helper: split relative parts from root -> track
func relParts(root, trackPath string) ([]string, error) {
	root = filepath.Clean(root)
	trackPath = filepath.Clean(trackPath)

	rel, err := filepath.Rel(root, trackPath)
	if err != nil {
		return nil, fmt.Errorf("cannot make %q relative to %q: %w", trackPath, root, err)
	}

	logger.Log.Trace("root path: " + root)
	logger.Log.Trace("track path: " + trackPath)

	// Guard against paths outside root (..)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "..") {
		return nil, fmt.Errorf("path %q is not under root %q", trackPath, root)
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	// must end with a filename (track)
	if len(parts) < 3 {
		return nil, fmt.Errorf("relative path %q too short; need artist/album/track (or artist/album/media/track)", rel)
	}
	// Very light sanity check: last part looks like a file (has an extension)
	if filepath.Ext(parts[len(parts)-1]) == "" {
		return nil, fmt.Errorf("last segment %q is not a file (no extension) in %q", parts[len(parts)-1], rel)
	}
	// Normalize segments for consistency (trim + NFC)
	for i := range parts {
		parts[i] = norm.NFC.String(strings.TrimSpace(parts[i]))
	}
	return parts, nil
}

// picks the artist folder name from /root/artist/album[/media]/track
func ExtractArtistNameFromTrackFilePath(root string, trackPath string) (string, error) {
	parts, err := relParts(root, trackPath)
	if err != nil {
		return "", err
	}
	artist := parts[0]
	if artist == "" {
		return "", fmt.Errorf("empty artist segment in %q", trackPath)
	}
	return artist, nil
}

// picks the album folder name from /root/artist/album[/media]/track
func ExtractAlbumNameFromTrackFilePath(root string, trackPath string) (string, error) {
	parts, err := relParts(root, trackPath)
	if err != nil {
		return "", err
	}
	album := parts[1]
	if album == "" {
		return "", fmt.Errorf("empty album segment in %q", trackPath)
	}
	return album, nil
}

// picks the media subdir (CD 01 / Vinyl 1 / Disc 2...) if present; returns "" if not present
func ExtractMediaNameFromTrackFilePath(root string, trackPath string) (string, error) {
	parts, err := relParts(root, trackPath)
	if err != nil {
		return "", err
	}
	// /root/artist/album/track        -> len(parts)==3 (no media)
	// /root/artist/album/media/track  -> len(parts)>=4 (media is parts[2])
	if len(parts) >= 4 {
		media := parts[2]
		if media == "" {
			return "", fmt.Errorf("empty media segment in %q", trackPath)
		}
		return media, nil
	}
	return "", nil // no media directory
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
