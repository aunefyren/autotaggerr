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

// single is the one-value case of a desired-tag map, for the fields that genuinely
// cannot hold more than one (a title, a date, a release MBID).
func single(value string) []string {
	if value == "" {
		return nil
	}
	return []string{value}
}

// renderFLACValues turns a field's values into the Vorbis comments the writer will
// emit: one comment per value, which is what the format means by a multi-valued
// field and what Picard, foobar2000, MusicBee, Kodi and Navidrome read natively.
//
// This costs the ffmpeg-backed players nothing, which is the measurement that made
// it worth doing: ffmpeg's FLAC demuxer joins repeated comments itself, so two GENRE
// comments reach Plex as "hip hop;rap" — the delimited string it wanted anyway, with
// no convention of ours involved. MP3 has no such luck (see renderMP3Values), so the
// two engines agree on which fields they write and differ only in how the format
// spells several values.
//
// Whatever this returns must be what the diff compares against, or the file is
// re-tagged on every scan forever.
func renderFLACValues(values []string) []string {
	return utilities.NormalizeTagValues(values)
}

// renderFLACTags applies renderFLACValues across a whole desired-tag map. Both the
// writer and the read-only diff view go through it, so what the UI previews is what a
// scan would write.
func renderFLACTags(desired map[string][]string) map[string][]string {
	rendered := make(map[string][]string, len(desired))
	for key, values := range desired {
		rendered[key] = renderFLACValues(values)
	}
	return rendered
}

// buildFLACDesiredTags maps resolved metadata onto the Vorbis comment keys we
// write. Kept pure (no I/O) so the field-to-tag wiring can be unit-tested.
//
// Values are carried as the several values they are; renderFLACValues decides how
// they reach the file. The engines used to disagree about that, and a FLAC library
// silently carried fewer genres than the same albums as MP3 for no reason anyone
// chose.
func buildFLACDesiredTags(metadata models.FileTags) map[string][]string {
	return map[string][]string{
		"ARTIST":      single(metadata.Artist),
		"ARTISTS":     metadata.Artists,
		"ALBUMARTIST": single(metadata.AlbumArtist),
		// ALBUMARTIST stays single-valued because Plex has no concept of several
		// artists and renders a joined string as one artist literally named "A; B" —
		// and because ffmpeg joins repeated Vorbis comments with ";" on read, that
		// stays true even once this engine writes the spec-correct form.
		// ALBUMARTISTS carries the full credit for players that can read it, exactly
		// as ARTISTS already sits beside ARTIST.
		"ALBUMARTISTS":               metadata.AlbumArtists,
		"GENRE":                      metadata.Genres,
		"DATE":                       single(metadata.ReleaseDate),
		"YEAR":                       single(metadata.ReleaseYear),
		"ORIGINALDATE":               single(metadata.OriginalDate),
		"ORIGINALYEAR":               single(metadata.OriginalYear),
		"RELEASEDATE":                single(metadata.ReleaseDate),
		"ALBUM":                      single(metadata.Album),
		"TITLE":                      single(metadata.Title),
		"TRACKNUMBER":                single(metadata.Track),
		"TRACKTOTAL":                 single(metadata.TrackTotal),
		"DISCNUMBER":                 single(metadata.DiscNumber),
		"DISCTOTAL":                  single(metadata.DiscTotal),
		"TOTALDISCS":                 single(metadata.DiscTotal),
		"ISRC":                       metadata.ISRCs,
		"RELEASESTATUS":              single(metadata.MBAlbumStatus),
		"RELEASETYPE":                single(metadata.MBAlbumType),
		"RELEASECOUNTRY":             single(metadata.MBAlbumReleaseCountry),
		"MUSICBRAINZ_ALBUMID":        single(metadata.MBAlbumID),
		"MUSICBRAINZ_ARTISTID":       metadata.MBArtistIDs,
		"MUSICBRAINZ_ALBUMARTISTID":  metadata.MBAlbumArtistIDs,
		"MUSICBRAINZ_RELEASEGROUPID": single(metadata.MBReleaseGroupID),
		"MUSICBRAINZ_RELEASETRACKID": single(metadata.MBReleaseTrackID),
		"MUSICBRAINZ_TRACKID":        single(metadata.MBRecordingID),
		"SCRIPT":                     single(metadata.Script),
		"LABEL":                      metadata.RecordLabels,
		"MEDIA":                      single(metadata.Media),
		"BARCODE":                    single(metadata.Barcode),
		"CATALOGNUMBER":              metadata.CatalogNumbers,
		// ASIN, COMPOSER and AUTHOR are deliberately absent: nothing ever resolved a
		// value for them, so listing them here only ever cleared whatever another
		// tagger had written once remove_values was on. Leaving them out means a
		// foreign tagger's value survives. MusicBrainz can supply composer (via work
		// relations) and ASIN (on the release) if they are ever wanted for real —
		// that is a fetch and a mapping, and it starts with models.FileTags.
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

	desired := renderFLACTags(buildFLACDesiredTags(metadata))

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
	for key, values := range changes {
		// remove then set only the keys that changed
		removeCmd := exec.Command("metaflac", "--no-utf8-convert", "--remove-tag="+key, filePath)
		if err := removeCmd.Run(); err != nil {
			logger.Log.Error(fmt.Sprintf("failed to remove tag %s: %s", key, err.Error()))
			return unchanged, tagsWritten, changed, errors.New("failed to remove tag")
		}

		// One --set-tag per value. A key with no values was cleared by the profile's
		// remove_values, and the --remove-tag above is the whole write — setting an
		// empty comment instead would leave a blank one behind for every cleared tag.
		for _, value := range values {
			setCmd := exec.Command("metaflac", "--no-utf8-convert", "--set-tag", fmt.Sprintf("%s=%s", key, value), filePath)
			if err := setCmd.Run(); err != nil {
				logger.Log.Error(fmt.Sprintf("failed to set tag %s: %s", key, err.Error()))
				return unchanged, tagsWritten, changed, errors.New("failed to set tag")
			}
		}

		tagsWritten++
		// Recorded only after the write succeeded, so the diff reports what is on
		// disk rather than what was intended.
		changed = append(changed, models.TagChange{
			Field: key,
			Old:   utilities.JoinTagValues(existing[key]),
			New:   utilities.JoinTagValues(values),
		})
	}

	utilities.SortTagChanges(changed)
	return unchanged, tagsWritten, changed, nil
}
