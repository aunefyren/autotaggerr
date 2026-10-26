package modules

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/utilities"
)

// Matching a file to Lidarr without trusting where it sits.
//
// Lidarr's answer is keyed by the file: the track file whose path is this file's. The
// path used to be read as <root>/<artist>/<album>/[<disc>/]<file> twice over — the
// artist folder to find the Lidarr artist, the album and disc folders to find the track
// file — so a file under a misspelled artist folder, one level too deep or too shallow,
// or under another artist's folder, was unmatched even though Lidarr has a track file at
// exactly that path. Both halves now degrade instead of failing:
//
//   - the track file is found by the file's path below the library root, compared from
//     the end (lidarrPathEndsWith), whatever its depth or names — the layout match stays
//     behind it as a fallback;
//   - the artist is found by the folder first, and, when that names no Lidarr artist or
//     one that does not hold the file, by the file's own tags: the MusicBrainz artist IDs
//     Lidarr knows its artists by, then their names (lidarrArtistsFromTags).

// lidarrRelativeSegments is a file's path below the library root, as segments, or nil
// when the file is not below it.
func lidarrRelativeSegments(rootDir, trackPath string) []string {
	if rootDir == "" {
		return nil
	}
	rel, err := filepath.Rel(rootDir, trackPath)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return nil
	}
	return strings.Split(filepath.ToSlash(rel), "/")
}

// lidarrPathEndsWith reports whether Lidarr's path for a track file ends with these
// segments — by whole segments, compared as Canon does, with either separator, since
// Lidarr's path is in its own host's form and under a root this host knows by another
// name. It is the same file at the same place below the root, which is what identifies
// it, however that place is laid out.
func lidarrPathEndsWith(lidarrPath string, rel []string) bool {
	if len(rel) == 0 {
		return false
	}
	parts := strings.FieldsFunc(lidarrPath, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) < len(rel) {
		return false
	}
	tail := parts[len(parts)-len(rel):]
	for i := range rel {
		if utilities.Canon(tail[i]) != utilities.Canon(rel[i]) {
			return false
		}
	}
	return true
}

// trackFileByRelativePath finds the one track file whose path ends with the file's
// path below the root. Two would mean the path does not identify it — only possible
// for a file directly in the root, where the name is all there is — and then nothing
// is returned, as the layout match does when it cannot decide.
func trackFileByRelativePath(files []models.LidarrTrackFile, rel []string) *models.LidarrTrackFile {
	var match *models.LidarrTrackFile
	for i := range files {
		if !lidarrPathEndsWith(files[i].Path, rel) {
			continue
		}
		if match != nil {
			logger.Log.Warnf("ambiguous Lidarr trackfile match for %q: both %q and %q end with it; refusing to guess",
				strings.Join(rel, "/"), match.Path, files[i].Path)
			return nil
		}
		match = &files[i]
	}
	return match
}

// allArtists is every artist Lidarr manages: the cached list while every entry of it is
// fresh, the API's otherwise (which refreshes the cache, as FindArtistByName does).
func (c *LidarrClient) allArtists() ([]models.LidarrArtist, error) {
	scope := c.cacheScope() + "|"
	var cached []models.LidarrArtist
	fresh := true
	lidarrArtistsCacheMu.RLock()
	for key, entry := range lidarrArtistsCache {
		if !strings.HasPrefix(key, scope) {
			continue
		}
		if time.Since(entry.Timestamp) >= lidarrArtistsCacheDuration {
			fresh = false
			break
		}
		cached = append(cached, entry.Artist)
	}
	lidarrArtistsCacheMu.RUnlock()
	if fresh && len(cached) > 0 {
		return cached, nil
	}
	return c.fetchArtists()
}

// lidarrArtistsFromTags names the Lidarr artists a file's own tags point at, skipping
// those already tried: first by the MusicBrainz artist IDs Lidarr knows its artists by
// (album artist before track artist), then by name. Tags that cannot be read, or that
// name no Lidarr artist, give none — the file stays unmatched, as it was.
func (c *LidarrClient) lidarrArtistsFromTags(trackPath string, tried map[int64]bool) ([]models.LidarrArtist, error) {
	tags, err := ReadFileTagMap(trackPath)
	if err != nil {
		logger.Log.Debugf("could not read the tags of %s to find its Lidarr artist: %s", trackPath, err.Error())
		return nil, nil
	}
	var ids, names []string
	// Vorbis comments and ID3 user frames name the same IDs differently.
	for _, key := range []string{"MUSICBRAINZ_ALBUMARTISTID", "MUSICBRAINZ ALBUM ARTIST ID", "MUSICBRAINZ_ARTISTID", "MUSICBRAINZ ARTIST ID"} {
		for _, v := range tags[key] {
			// One frame may hold several, joined by whichever separator the writer
			// used; an MBID contains none of them.
			ids = append(ids, strings.FieldsFunc(v, func(r rune) bool { return r == ';' || r == '/' || r == 0 })...)
		}
	}
	for _, key := range []string{"ALBUMARTIST", "ALBUM ARTIST", "ARTIST"} {
		names = append(names, tags[key]...)
	}
	if len(ids) == 0 && len(names) == 0 {
		return nil, nil
	}

	artists, err := c.allArtists()
	if err != nil {
		return nil, err
	}
	var out []models.LidarrArtist
	add := func(a models.LidarrArtist) {
		if !tried[a.ID] {
			tried[a.ID] = true
			out = append(out, a)
		}
	}
	for _, id := range ids {
		for _, a := range artists {
			if id != "" && strings.EqualFold(a.ForeignArtistID, strings.TrimSpace(id)) {
				add(a)
			}
		}
	}
	for _, name := range names {
		for _, a := range artists {
			if utilities.EqLoose(a.Name, name) || utilities.EqLoose(lidarrArtistFolder(a), name) {
				add(a)
			}
		}
	}
	return out, nil
}
