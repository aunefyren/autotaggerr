package modules

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/utilities"
	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/meta"
)

// ExtractFLACTag returns the value of a Vorbis comment key (case-insensitive).
// If key is empty, it will resolve from metadataType (e.g., "release" => MUSICBRAINZ_ALBUMID).
func ExtractFLACTag(filePath, key, metadataType string) (string, error) {
	if key == "" {
		var ok bool
		key, ok = utilities.MBVorbisKeyFor(metadataType)
		if !ok {
			return "", errors.New("unsupported or empty key/metadataType")
		}
	}
	key = strings.ToUpper(key)

	tags, err := getFlacTagsMap(filePath) // read all once
	if err != nil {
		return "", err
	}

	// return first non-empty match (Vorbis comments may have duplicates)
	if vals, ok := tags[key]; ok {
		for _, v := range vals {
			v = utilities.NormalizeTagValue(v)
			if v != "" {
				return v, nil
			}
		}
	}
	return "", nil
}

// getFlacTagsMap returns all Vorbis comments as KEY -> []values (uppercased keys).
func getFlacTagsMap(filePath string) (map[string][]string, error) {
	stream, err := flac.ParseFile(filePath)
	if err != nil {
		return nil, err
	}

	out := make(map[string][]string)
	for _, block := range stream.Blocks {
		if vc, ok := block.Body.(*meta.VorbisComment); ok {
			for _, kv := range vc.Tags {
				if len(kv) < 2 {
					continue
				}
				key := strings.ToUpper(strings.TrimSpace(kv[0]))
				val := utilities.NormalizeTagValue(kv[1])
				out[key] = append(out[key], val)
			}
		}
	}
	return out, nil
}

// buildFLACDesiredTags maps resolved metadata onto the Vorbis comment keys we
// write. Kept pure (no I/O) so the field-to-tag wiring can be unit-tested.
// Note: FLAC writes only the first genre (Vorbis GENRE), unlike MP3 which joins.
func buildFLACDesiredTags(metadata models.FileTags) map[string]string {
	genreString := ""
	if len(metadata.Genres) > 0 {
		genreString = metadata.Genres[0]
	}

	return map[string]string{
		"ARTIST":                     metadata.Artist,
		"ARTISTS":                    metadata.ArtistSemicolon,
		"ALBUMARTIST":                metadata.AlbumArtist,
		"GENRE":                      genreString,
		"DATE":                       metadata.ReleaseDate,
		"YEAR":                       metadata.ReleaseYear,
		"ORIGINALDATE":               metadata.OriginalDate,
		"ORIGINALYEAR":               metadata.OriginalYear,
		"RELEASEDATE":                metadata.ReleaseDate,
		"ALBUM":                      metadata.Album,
		"TITLE":                      metadata.Title,
		"TRACKNUMBER":                metadata.Track,
		"TRACKTOTAL":                 metadata.TrackTotal,
		"DISCNUMBER":                 metadata.DiscNumber,
		"DISCTOTAL":                  metadata.DiscTotal,
		"TOTALDISCS":                 metadata.DiscTotal,
		"ISRC":                       metadata.ISRC,
		"RELEASESTATUS":              metadata.MBAlbumStatus,
		"RELEASETYPE":                metadata.MBAlbumType,
		"RELEASECOUNTRY":             metadata.MBAlbumReleaseCountry,
		"MUSICBRAINZ_ALBUMID":        metadata.MBAlbumID,
		"MUSICBRAINZ_ARTISTID":       metadata.MBArtistID,
		"MUSICBRAINZ_ALBUMARTISTID":  metadata.MBAlbumArtistID,
		"MUSICBRAINZ_RELEASEGROUPID": metadata.MBReleaseGroupID,
		"MUSICBRAINZ_RELEASETRACKID": metadata.MBReleaseTrackID,
		"MUSICBRAINZ_TRACKID":        metadata.MBRecordingID,
		"SCRIPT":                     metadata.Script,
		"LABEL":                      metadata.RecordLabel,
		"MEDIA":                      metadata.Media,
		"BARCODE":                    metadata.Barcode,
		"CATALOGNUMBER":              metadata.CatalogNumber,
		"ASIN":                       metadata.ASIN,
		"COMPOSER":                   metadata.Composer,
		"AUTHOR":                     metadata.Author,
	}
}

// SetFlacTags updates multiple Vorbis comment tags on a FLAC file. The returned
// changes are the field-level before/after of what was written — the Activity feed's
// per-file detail; see models.TagChange.
func SetFlacTags(filePath string, metadata models.FileTags, configFile models.ConfigStruct) (unchanged bool, tagsWritten int, changed []models.TagChange, err error) {
	unchanged = false
	tagsWritten = 0

	filePath, err = utilities.NormalizePathForExternalTool(filePath)
	if err != nil {
		logger.Log.Error("failed to normalize path. error: " + err.Error())
		return unchanged, tagsWritten, nil, errors.New("failed to normalize path")
	}

	desired := buildFLACDesiredTags(metadata)

	existing, err := getFlacTagsMap(filePath)
	if err != nil {
		// Optional: keep going even if read fails, or return error
		return unchanged, tagsWritten, nil, err
	}

	changes, hasChanges := utilities.DiffFlacTags(existing, desired, configFile)
	if !hasChanges {
		logger.Log.Debug("no tag changes needed: " + filePath)
		return true, tagsWritten, nil, nil
	}

	// --no-utf8-convert tells metaflac the tag arguments are already UTF-8 and must be
	// stored verbatim. Our Go strings are always UTF-8, so this is correct — and it is
	// locale-independent. Without it, metaflac converts from the process's "local
	// charset" to UTF-8, and on a host where that locale resolves to ASCII/C (a minimal
	// container, or any box without en_US.UTF-8 generated) every non-ASCII byte is
	// replaced with '#'. Forcing LANG/LC_ALL=en_US.UTF-8 here used to *cause* that,
	// because a locale that is not installed falls back to C.
	for key, value := range changes {
		// remove then set only the keys that changed
		removeCmd := exec.Command("metaflac", "--no-utf8-convert", "--remove-tag="+key, filePath)
		if err := removeCmd.Run(); err != nil {
			logger.Log.Error(fmt.Sprintf("failed to remove tag %s: %s", key, err.Error()))
			return unchanged, tagsWritten, changed, errors.New("failed to remove tag")
		}

		setCmd := exec.Command("metaflac", "--no-utf8-convert", "--set-tag", fmt.Sprintf("%s=%s", key, value), filePath)
		if err := setCmd.Run(); err != nil {
			logger.Log.Error(fmt.Sprintf("failed to set tag %s: %s", key, err.Error()))
			return unchanged, tagsWritten, changed, errors.New("failed to set tag")
		} else {
			tagsWritten++
			// Recorded only after the write succeeded, so the diff reports what is on
			// disk rather than what was intended.
			changed = append(changed, models.TagChange{
				Field: key,
				Old:   strings.Join(existing[key], "; "),
				New:   value,
			})
		}
	}

	utilities.SortTagChanges(changed)
	return unchanged, tagsWritten, changed, nil
}
