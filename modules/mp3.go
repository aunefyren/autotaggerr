package modules

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/utilities"
	"github.com/bogem/id3v2"
)

// buildMP3DesiredTags maps resolved metadata onto the ID3 keys we write. Kept
// pure (no I/O) so the field-to-tag wiring can be unit-tested. Note: MP3 joins
// all genres with ";", unlike FLAC which writes only the first.
func buildMP3DesiredTags(metadata models.FileTags) map[string]string {
	genreString := ""
	for index, genre := range metadata.Genres {
		if index != 0 {
			genreString += ";"
		}
		genreString += genre
	}

	return map[string]string{
		"ARTIST":      metadata.Artist,
		"ARTISTS":     metadata.ArtistSemicolon,
		"ALBUMARTIST": metadata.AlbumArtist,
		"GENRE":       genreString,
		"ALBUM":       metadata.Album,
		"TITLE":       metadata.Title,
		"TRACKNUMBER": metadata.Track,
		"DISCNUMBER":  metadata.DiscNumber,

		// Totals ride along in the composite TRCK/TPOS frames ("3/12", "1/2")
		// built below — the standard ID3 representation, which ffprobe reads back.
		"TRACKTOTAL": metadata.TrackTotal,
		"DISCTOTAL":  metadata.DiscTotal,
		// ISRC is written as a TXXX:ISRC frame (see below) and decoded by GetMP3Tags.
		"ISRC": metadata.ISRC,

		"SCRIPT":    metadata.Script,
		"TMED":      metadata.Media,
		"publisher": metadata.RecordLabel,

		// Release
		"DATE": metadata.ReleaseDate, // maps to TDRC
		"YEAR": metadata.ReleaseYear, // maps to TYER

		// Original release
		"TDOR":         metadata.OriginalDate, // maps to TDOR
		"originaldate": metadata.OriginalDate,
		"originalyear": metadata.OriginalYear,

		// MUSICBRAINZ
		"MusicBrainz Album Status":          metadata.MBAlbumStatus,
		"MusicBrainz Album Type":            metadata.MBAlbumType,
		"MusicBrainz Album Release Country": metadata.MBAlbumReleaseCountry,
		"MusicBrainz Album Id":              metadata.MBAlbumID,
		"MusicBrainz Artist Id":             metadata.MBArtistID,
		"MusicBrainz Album Artist Id":       metadata.MBAlbumArtistID,
		"MusicBrainz Release Group Id":      metadata.MBReleaseGroupID,
		"MusicBrainz Release Track Id":      metadata.MBReleaseTrackID,
	}
}

// pairedFrameValue renders the "n/total" halves of a paired ID3 frame (track, disc).
// An empty total drops the suffix, and an empty number yields "" — which is how the
// frame is cleared, since the pair shares one frame and cannot be half-removed.
func pairedFrameValue(number, total string) string {
	if number == "" {
		return ""
	}
	if total == "" {
		return number
	}
	return fmt.Sprintf("%s/%s", number, total)
}

// SetMP3Tags writes ID3 metadata with ffmpeg. The returned changes are the
// field-level before/after actually applied; see models.TagChange.
//
// Every key in the change set must reach an -metadata argument, including the ones
// whose desired value is empty (which the profile's remove_values turns into a
// change): ffmpeg deletes a tag when given an empty value, and the reported diff is
// derived from the change set, so a key reported but not written would be re-reported
// on every scan forever. That is why the write blocks below do not second-guess an
// empty value.
func SetMP3Tags(filePath string, metadata models.FileTags, configFile models.ConfigStruct) (unchanged bool, tagsWritten int, changed []models.TagChange, err error) {
	unchanged = false
	tagsWritten = 0

	originalFilePath := filePath
	filePath, err = utilities.NormalizePathForExternalTool(filePath)
	if err != nil {
		logger.Log.Error("failed to normalize path. error: " + err.Error())
		return unchanged, tagsWritten, nil, errors.New("failed to normalize path")
	}

	desired := buildMP3DesiredTags(metadata)

	logger.Log.Debug(desired)

	existing, err := GetMP3Tags(filePath)
	if err != nil {
		return false, 0, nil, fmt.Errorf("read mp3 tags failed: %w", err)
	}

	logger.Log.Debug(existing)

	changes, hasChanges := utilities.DiffID3Tags(existing, desired, configFile)
	if !hasChanges {
		logger.Log.Debug("no tag changes, returning")
		return true, 0, nil, nil // unchanged
	} else {
		logger.Log.Debug("found tag changes")
		logger.Log.Debug(changes)
	}

	// Build ffmpeg args; only set changed fields (plus paired composite fields)
	args := []string{
		"-i", filePath,
		"-y",
		"-map_metadata", "0",
		"-codec", "copy",
		"-write_id3v1", "1", // legacy fallback
		"-id3v2_version", "4", // prefer v2.4 (gives TDOR/TDRC)
	}

	addMeta := func(k, v string) {
		args = append(args, "-metadata", fmt.Sprintf("%s=%s", k, v))
	}

	// Simple 1:1 fields
	if _, ok := changes["ARTIST"]; ok {
		logger.Log.Trace("adding ARTIST")
		addMeta("artist", desired["ARTIST"])
		tagsWritten++
	}
	if _, ok := changes["ARTISTS"]; ok {
		logger.Log.Trace("adding ARTISTS")
		addMeta("ARTISTS", desired["ARTISTS"])
		tagsWritten++
	}
	if _, ok := changes["ALBUMARTIST"]; ok {
		logger.Log.Trace("adding ALBUMARTIST")
		addMeta("album_artist", desired["ALBUMARTIST"])
		tagsWritten++
	}
	if _, ok := changes["GENRE"]; ok {
		logger.Log.Trace("adding GENRE")
		addMeta("genre", desired["GENRE"])
		tagsWritten++
	}

	// Release date/year
	if _, ok := changes["DATE"]; ok {
		logger.Log.Trace("adding DATE")
		addMeta("date", desired["DATE"])
		tagsWritten++
	}
	if _, ok := changes["YEAR"]; ok {
		logger.Log.Trace("adding YEAR")
		addMeta("year", desired["YEAR"])
		tagsWritten++
	}

	// Original release date/year
	if _, ok := changes["TDOR"]; ok {
		logger.Log.Trace("adding TDOR")
		addMeta("TDOR", desired["TDOR"])
		tagsWritten++
	}
	if _, ok := changes["ORIGINALDATE"]; ok {
		logger.Log.Trace("adding originaldate")
		addMeta("originaldate", desired["originaldate"])
		tagsWritten++
	}
	if _, ok := changes["ORIGINALYEAR"]; ok {
		logger.Log.Trace("adding originalyear")
		addMeta("originalyear", desired["originalyear"])
		tagsWritten++
	}

	// additional
	if _, ok := changes["ALBUM"]; ok {
		logger.Log.Trace("adding ALBUM")
		addMeta("album", desired["ALBUM"])
		tagsWritten++
	}
	if _, ok := changes["TITLE"]; ok {
		logger.Log.Trace("adding TITLE")
		addMeta("title", desired["TITLE"])
		tagsWritten++
	}
	if _, ok := changes["SCRIPT"]; ok {
		logger.Log.Trace("adding SCRIPT")
		addMeta("SCRIPT", desired["SCRIPT"])
		tagsWritten++
	}
	if _, ok := changes["TMED"]; ok {
		logger.Log.Trace("adding TMED")
		addMeta("TMED", desired["TMED"])
		tagsWritten++
	}
	if _, ok := changes["PUBLISHER"]; ok {
		logger.Log.Trace("adding publisher")
		addMeta("publisher", desired["publisher"])
		tagsWritten++
	}

	// MusicBrainz tags
	if _, ok := changes["MUSICBRAINZ ALBUM STATUS"]; ok {
		logger.Log.Trace("adding MusicBrainz Album Status")
		addMeta("MusicBrainz Album Status", desired["MusicBrainz Album Status"])
		tagsWritten++
	}
	if _, ok := changes["MUSICBRAINZ ALBUM TYPE"]; ok {
		logger.Log.Trace("adding MusicBrainz Album Type")
		addMeta("MusicBrainz Album Type", desired["MusicBrainz Album Type"])
		tagsWritten++
	}
	if _, ok := changes["MUSICBRAINZ ALBUM RELEASE COUNTRY"]; ok {
		logger.Log.Trace("adding MusicBrainz Album Release Country")
		addMeta("MusicBrainz Album Release Country", desired["MusicBrainz Album Release Country"])
		tagsWritten++
	}
	if _, ok := changes["MUSICBRAINZ ALBUM ID"]; ok {
		logger.Log.Trace("adding MusicBrainz Album Id")
		addMeta("MusicBrainz Album Id", desired["MusicBrainz Album Id"])
		tagsWritten++
	}
	if _, ok := changes["MUSICBRAINZ ARTIST ID"]; ok {
		logger.Log.Tracef("adding MusicBrainz Artist Id, have %s, want %s", existing["MusicBrainz Artist Id"], desired["MusicBrainz Artist Id"])
		addMeta("MusicBrainz Artist Id", desired["MusicBrainz Artist Id"])
		tagsWritten++
	}
	if _, ok := changes["MUSICBRAINZ ALBUM ARTIST ID"]; ok {
		logger.Log.Trace("adding MusicBrainz Album Artist Id")
		addMeta("MusicBrainz Album Artist Id", desired["MusicBrainz Album Artist Id"])
		tagsWritten++
	}
	if _, ok := changes["MUSICBRAINZ RELEASE GROUP ID"]; ok {
		logger.Log.Trace("adding MusicBrainz Release Group Id")
		addMeta("MusicBrainz Release Group Id", desired["MusicBrainz Release Group Id"])
		tagsWritten++
	}
	if _, ok := changes["MUSICBRAINZ RELEASE TRACK ID"]; ok {
		logger.Log.Trace("adding MusicBrainz Release Track Id")
		addMeta("MusicBrainz Release Track Id", desired["MusicBrainz Release Track Id"])
		tagsWritten++
	}

	// Composite: track (TRACKNUMBER/TRACKTOTAL). Both halves live in one ID3 frame, so
	// either one changing rewrites the pair — and both being empty clears the frame.
	_, trackNumberChanged := changes["TRACKNUMBER"]
	_, trackTotalChanged := changes["TRACKTOTAL"]
	if trackNumberChanged || trackTotalChanged {
		logger.Log.Trace("adding track")
		addMeta("track", pairedFrameValue(desired["TRACKNUMBER"], desired["TRACKTOTAL"]))
		if trackNumberChanged {
			tagsWritten++
		}
		if trackTotalChanged {
			tagsWritten++
		}
	}

	// Composite: disc (DISCNUMBER/DISCTOTAL)
	_, discNumberChanged := changes["DISCNUMBER"]
	_, discTotalChanged := changes["DISCTOTAL"]
	if discNumberChanged || discTotalChanged {
		logger.Log.Trace("adding disc")
		addMeta("disc", pairedFrameValue(desired["DISCNUMBER"], desired["DISCTOTAL"]))
		if discNumberChanged {
			tagsWritten++
		}
		if discTotalChanged {
			tagsWritten++
		}
	}

	// Custom TXXX frames. ffmpeg stores an unknown metadata key as a TXXX frame, so
	// we encode "ISRC:<value>"; GetMP3Tags decodes it back into the ISRC key. Clearing
	// it must empty the frame itself ("TXXX=") rather than write an empty payload
	// ("TXXX=ISRC:"), which would leave a junk frame behind on every cleared file.
	if _, ok := changes["ISRC"]; ok {
		logger.Log.Trace("adding ISRC")
		if desired["ISRC"] == "" {
			args = append(args, "-metadata", "TXXX=")
		} else {
			args = append(args, "-metadata", "TXXX=ISRC:"+desired["ISRC"])
		}
		tagsWritten++
	}

	tempOutput := filePath + ".temp.mp3"
	args = append(args, tempOutput)

	cmd := exec.Command("ffmpeg", args...)
	// Ensure UTF-8 env if you’ve used that elsewhere:
	cmd.Env = append(os.Environ(), "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8")
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout

	if err := cmd.Run(); err != nil {
		return false, 0, nil, fmt.Errorf("ffmpeg tagging failed: %w", err)
	}
	if err := os.Rename(tempOutput, originalFilePath); err != nil {
		return false, 0, nil, fmt.Errorf("failed to replace original file: %w", err)
	}

	// The diff is derived from the change set rather than from the write blocks
	// above: ffmpeg rewrites the whole file in one pass, so there is no per-field
	// success to report, and `changes` is already exactly the set of fields that
	// differed. tagsWritten can exceed this count — a changed DISCNUMBER also
	// rewrites its paired DISCTOTAL.
	changed = make([]models.TagChange, 0, len(changes))
	for key, value := range changes {
		changed = append(changed, models.TagChange{
			Field: key,
			Old:   strings.Join(existing[key], "; "),
			New:   value,
		})
	}
	utilities.SortTagChanges(changed)

	return false, tagsWritten, changed, nil
}

func GetMP3Tags(filePath string) (map[string][]string, error) {
	cmd := exec.Command("ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		filePath,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe failed: %w", err)
	}
	var fp models.FfprobeFormat
	if err := json.Unmarshal(out, &fp); err != nil {
		return nil, fmt.Errorf("ffprobe parse failed: %w", err)
	}
	res := make(map[string][]string)
	for k, v := range fp.Format.Tags {
		key := strings.ToLower(strings.TrimSpace(k))
		val := utilities.NormalizeTagValue(v)

		switch key {
		case "artist":
			res["ARTIST"] = append(res["ARTIST"], val)
		case "album_artist":
			res["ALBUMARTIST"] = append(res["ALBUMARTIST"], val)
		case "genre":
			res["GENRE"] = append(res["GENRE"], val)
		case "date", "tdrc":
			res["DATE"] = append(res["DATE"], val)
		case "year", "tyer":
			res["YEAR"] = append(res["YEAR"], val)
		case "originaldate":
			res["ORIGINALDATE"] = append(res["ORIGINALDATE"], val)
		case "tory", "originalyear", "original_year":
			res["ORIGINALYEAR"] = append(res["ORIGINALYEAR"], val)
		case "album":
			res["ALBUM"] = append(res["ALBUM"], val)
		case "title":
			res["TITLE"] = append(res["TITLE"], val)
		case "track":
			// e.g. "3/12" or "3"
			parts := strings.SplitN(val, "/", 2)
			if len(parts) >= 1 {
				res["TRACKNUMBER"] = append(res["TRACKNUMBER"], utilities.NormalizeTagValue(parts[0]))
			}
			if len(parts) == 2 {
				res["TRACKTOTAL"] = append(res["TRACKTOTAL"], utilities.NormalizeTagValue(parts[1]))
			}
		case "disc":
			parts := strings.SplitN(val, "/", 2)
			if len(parts) >= 1 {
				res["DISCNUMBER"] = append(res["DISCNUMBER"], utilities.NormalizeTagValue(parts[0]))
			}
			if len(parts) == 2 {
				res["DISCTOTAL"] = append(res["DISCTOTAL"], utilities.NormalizeTagValue(parts[1]))
			}
		case "script":
			res[strings.ToUpper(key)] = append(res[strings.ToUpper(key)], val)
		case "artists":
			res[strings.ToUpper(key)] = append(res[strings.ToUpper(key)], val)
		case "tdor":
			res[strings.ToUpper(key)] = append(res[strings.ToUpper(key)], val)
		case "tmed":
			res[strings.ToUpper(key)] = append(res[strings.ToUpper(key)], val)
		case "publisher":
			res[strings.ToUpper(key)] = append(res[strings.ToUpper(key)], val)
		case "musicbrainz release group id":
			res[strings.ToUpper(key)] = append(res[strings.ToUpper(key)], val)
		case "musicbrainz album type":
			res[strings.ToUpper(key)] = append(res[strings.ToUpper(key)], val)
		case "musicbrainz album release country":
			res[strings.ToUpper(key)] = append(res[strings.ToUpper(key)], val)
		case "musicbrainz album status":
			res[strings.ToUpper(key)] = append(res[strings.ToUpper(key)], val)
		case "musicbrainz album id":
			res[strings.ToUpper(key)] = append(res[strings.ToUpper(key)], val)
		case "musicbrainz artist id":
			res[strings.ToUpper(key)] = append(res[strings.ToUpper(key)], val)
		case "musicbrainz album artist id":
			res[strings.ToUpper(key)] = append(res[strings.ToUpper(key)], val)
		case "musicbrainz release track id":
			res[strings.ToUpper(key)] = append(res[strings.ToUpper(key)], val)
		default:
			logger.Log.Debugf("reading MP3 tag: key %s, val %s", key, val)
			// Handle TXXX:* custom frames (e.g., TXXX:ISRC). We pack these as a
			// single "KEY:value" string, so split on the FIRST colon only — the
			// value itself may contain the separators we use for multi-value tags
			// (e.g. a "; "-joined ISRC list). Splitting on ";"/all colons truncated
			// multi-value ISRCs to their first entry, so those tracks never matched
			// the desired tags and were rewritten on every scan.
			if strings.HasPrefix(strings.ToUpper(key), "TXXX") {
				kv := strings.SplitN(val, ":", 2)
				if len(kv) == 2 {
					// Upper-case the key (it must match the res map keys /
					// case labels) but preserve the value's original case.
					custom := strings.ToUpper(strings.TrimSpace(kv[0]))
					customValue := utilities.NormalizeTagValue(kv[1])
					switch custom {
					case "ISRC", "TRACKTOTAL", "DISCTOTAL":
						res[custom] = append(res[custom], customValue)
					}
				}
			}
		}
	}
	return res, nil
}

func extractFromID3v2(filePath string, metadataType string) (string, error) {
	var keyName string
	switch metadataType {
	case "release":
		keyName = "MusicBrainz Album Id"
	case "release_group":
		keyName = "MusicBrainz Release Group Id"
	case "track":
		keyName = "MusicBrainz Release Track Id"
	case "recording":
		keyName = "MusicBrainz Recording Id"
	case "title":
		keyName = "title"
	// add others if needed
	default:
		return "", errors.New("unsupported tag name for media type")
	}

	tagFile, err := id3v2.Open(filePath, id3v2.Options{Parse: true})
	if err != nil {
		return "", err
	}
	defer tagFile.Close()

	// simple tags
	switch keyName {
	case "title":
		for _, frame := range tagFile.GetFrames("TIT2") {
			if tf, ok := frame.(id3v2.TextFrame); ok {
				return strings.TrimSpace(tf.Text), nil
			}
		}
		return "", nil
	}

	// check for TXXX tags
	for _, frame := range tagFile.GetFrames("TXXX") {
		if uf, ok := frame.(id3v2.UserDefinedTextFrame); ok {
			if strings.EqualFold(strings.TrimSpace(uf.Description), keyName) {
				return strings.TrimSpace(uf.Value), nil
			}
		}
	}
	return "", nil
}
