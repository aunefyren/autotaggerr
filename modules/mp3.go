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

		/* to be added later
		"TRACKTOTAL":  metadata.TrackTotal,
		"DISCTOTAL":   metadata.DiscTotal,
		"ISRC":        metadata.ISRC,
		*/

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

func SetMP3Tags(filePath string, metadata models.FileTags) (unchanged bool, tagsWritten int, err error) {
	unchanged = false
	tagsWritten = 0

	originalFilePath := filePath
	filePath, err = utilities.NormalizePathForExternalTool(filePath)
	if err != nil {
		logger.Log.Error("failed to normalize path. error: " + err.Error())
		return unchanged, tagsWritten, errors.New("failed to normalize path")
	}

	desired := buildMP3DesiredTags(metadata)

	logger.Log.Debug(desired)

	existing, err := GetMP3Tags(filePath)
	if err != nil {
		return false, 0, fmt.Errorf("read mp3 tags failed: %w", err)
	}

	logger.Log.Debug(existing)

	changes, hasChanges := utilities.DiffID3Tags(existing, desired)
	if !hasChanges {
		logger.Log.Debug("no tag changes, returning")
		return true, 0, nil // unchanged
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
	if _, ok := changes["ARTISTS"]; ok && desired["ARTISTS"] != "" {
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
	if _, ok := changes["TDOR"]; ok && desired["TDOR"] != "" {
		logger.Log.Trace("adding TDOR")
		addMeta("TDOR", desired["TDOR"])
		tagsWritten++
	}
	if _, ok := changes["ORIGINALDATE"]; ok && desired["originaldate"] != "" {
		logger.Log.Trace("adding originaldate")
		addMeta("originaldate", desired["originaldate"])
		tagsWritten++
	}
	if _, ok := changes["ORIGINALYEAR"]; ok && desired["originalyear"] != "" {
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

	// Composite: track (TRACKNUMBER/TRACKTOTAL)
	if _, nChanged := changes["TRACKNUMBER"]; nChanged || changes["TRACKTOTAL"] != "" {
		tn := desired["TRACKNUMBER"]
		tt := desired["TRACKTOTAL"]
		if tn != "" && tt != "" {
			logger.Log.Trace("adding track")
			addMeta("track", fmt.Sprintf("%s/%s", tn, tt))
		} else if tn != "" {
			logger.Log.Trace("adding track")
			addMeta("track", tn)
		}
		if nChanged {
			tagsWritten++
		}
		if _, tChanged := changes["TRACKTOTAL"]; tChanged {
			tagsWritten++
		}
	}

	// Composite: disc (DISCNUMBER/DISCTOTAL)
	if _, nChanged := changes["DISCNUMBER"]; nChanged || changes["DISCTOTAL"] != "" {
		dn := desired["DISCNUMBER"]
		dt := desired["DISCTOTAL"]
		if dn != "" && dt != "" {
			logger.Log.Trace("adding disc")
			addMeta("disc", fmt.Sprintf("%s/%s", dn, dt))
		} else if dn != "" {
			logger.Log.Trace("adding disc")
			addMeta("disc", dn)
		}
		if nChanged {
			tagsWritten++
		}
		if _, tChanged := changes["DISCTOTAL"]; tChanged {
			tagsWritten++
		}
	}

	// Custom TXXX frames
	if _, ok := changes["ISRC"]; ok && desired["ISRC"] != "" {
		logger.Log.Trace("adding ISRC")
		addMeta("TXXX=ISRC:"+desired["ISRC"], "")
		// ffmpeg expects "TXXX=KEY:VALUE" as one value; we pass via previous call format:
		args[len(args)-1] = fmt.Sprintf("TXXX=ISRC:%s", desired["ISRC"])
		tagsWritten++
	}
	if _, ok := changes["TRACKTOTAL"]; ok && desired["TRACKTOTAL"] != "" {
		logger.Log.Trace("adding TRACKTOTAL")
		args = append(args, "-metadata", fmt.Sprintf("TXXX=TRACKTOTAL:%s", desired["TRACKTOTAL"]))
	}
	if _, ok := changes["DISCTOTAL"]; ok && desired["DISCTOTAL"] != "" {
		logger.Log.Trace("adding DISCTOTAL")
		args = append(args, "-metadata", fmt.Sprintf("TXXX=DISCTOTAL:%s", desired["DISCTOTAL"]))
	}

	tempOutput := filePath + ".temp.mp3"
	args = append(args, tempOutput)

	cmd := exec.Command("ffmpeg", args...)
	// Ensure UTF-8 env if you’ve used that elsewhere:
	cmd.Env = append(os.Environ(), "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8")
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout

	if err := cmd.Run(); err != nil {
		return false, 0, fmt.Errorf("ffmpeg tagging failed: %w", err)
	}
	if err := os.Rename(tempOutput, originalFilePath); err != nil {
		return false, 0, fmt.Errorf("failed to replace original file: %w", err)
	}

	return false, tagsWritten, nil
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
			// Handle TXXX:* custom frames (e.g., TXXX:ISRC)
			if strings.HasPrefix(strings.ToUpper(key), "TXXX") {
				txxxSplit := strings.Split(val, ";")

				for _, txxxEntry := range txxxSplit {
					txxxEntrySplit := strings.Split(txxxEntry, ":")
					if len(txxxEntrySplit) == 2 {
						custom := strings.ToUpper(strings.TrimSpace(txxxEntrySplit[0]))
						customValue := strings.ToUpper(strings.TrimSpace(txxxEntrySplit[1]))
						switch custom {
						case "ISRC", "TRACKTOTAL", "DISCTOTAL":
							res[custom] = append(res[custom], customValue)
						}
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
