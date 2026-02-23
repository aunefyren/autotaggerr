package modules

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/utilities"
	"github.com/bogem/id3v2"
	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/meta"
)

// List of allowed audio file extensions
var supportedExtensions = map[string]bool{
	".flac": true,
	".mp3":  true,
	".m4a":  false,
	".ogg":  false,
	".wav":  false,
}

// extractMusicBrainzReleaseID extracts the MusicBrainz Album ID from either MP3 (ID3v2) or FLAC (Vorbis)
func ExtractMusicBrainzReleaseID(filePath string) (string, error) {
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".mp3":
		return extractFromID3v2(filePath, "release")
	case ".flac":
		return ExtractFLACTag(filePath, "", "release")
	default:
		return "", errors.New("unsupported file type")
	}
}

// extractMusicBrainzReleaseID extracts the MusicBrainz Track ID from either MP3 (ID3v2) or FLAC (Vorbis)
func ExtractMusicBrainzTrackID(filePath string) (string, error) {
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".mp3":
		return extractFromID3v2(filePath, "track")
	case ".flac":
		return ExtractFLACTag(filePath, "", "track")
	default:
		return "", errors.New("unsupported file type")
	}
}

// extractMusicBrainzReleaseID extracts the MusicBrainz Recording ID from either MP3 (ID3v2) or FLAC (Vorbis)
func ExtractMusicBrainzRecordingID(filePath string) (string, error) {
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".mp3":
		return extractFromID3v2(filePath, "recording")
	case ".flac":
		return ExtractFLACTag(filePath, "", "recording")
	default:
		return "", errors.New("unsupported file type")
	}
}

// extractMusicBrainzReleaseID extracts the track titlle from either MP3 (ID3v2) or FLAC (Vorbis)
func ExtractTrackTitle(filePath string) (string, error) {
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".mp3":
		return extractFromID3v2(filePath, "title")
	case ".flac":
		return ExtractFLACTag(filePath, "title", "")
	default:
		return "", errors.New("unsupported file type")
	}
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

// Write MusicBrainz Album ID to an MP3 tag
func writeMusicBrainzAlbumIDToID3v2(mp3Path, mbid string) error {
	tagFile, err := id3v2.Open(mp3Path, id3v2.Options{Parse: true})
	if err != nil {
		return err
	}
	defer tagFile.Close()

	// Create UserDefinedTextFrame
	udtf := id3v2.UserDefinedTextFrame{
		Description: "MusicBrainz Album Id",
		Value:       mbid,
	}

	// Add or overwrite the frame
	tagFile.AddFrame(tagFile.CommonID("UserDefinedText"), udtf)

	// Save changes
	if err := tagFile.Save(); err != nil {
		return err
	}

	return nil
}

func SetFileTags(filePath string, metadata models.FileTags, configFile models.ConfigStruct) (unchanged bool, tagsWritten int, err error) {
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".mp3":
		return SetMP3Tags(filePath, metadata)
	case ".flac":
		return SetFlacTags(filePath, metadata, configFile)
	default:
		return false, 0, errors.New("unsupported file type")
	}
}

// SetFlacTags updates multiple Vorbis comment tags on a FLAC file.
func SetFlacTags(filePath string, metadata models.FileTags, configFile models.ConfigStruct) (unchanged bool, tagsWritten int, err error) {
	unchanged = false
	tagsWritten = 0

	genreString := ""
	if len(metadata.Genres) > 0 {
		genreString = metadata.Genres[0]
	}

	desired := map[string]string{
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
	}

	existing, err := getFlacTagsMap(filePath)
	if err != nil {
		// Optional: keep going even if read fails, or return error
		return unchanged, tagsWritten, err
	}

	changes, hasChanges := utilities.DiffFlacTags(existing, desired, configFile)
	if !hasChanges {
		logger.Log.Debug("no tag changes needed: " + filePath)
		return true, tagsWritten, nil
	}

	utf8Env := append(os.Environ(), "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8")

	for key, value := range changes {
		// remove then set only the keys that changed
		removeCmd := exec.Command("metaflac", "--remove-tag="+key, filePath)
		removeCmd.Env = utf8Env
		if err := removeCmd.Run(); err != nil {
			logger.Log.Error(fmt.Sprintf("failed to remove tag %s: %s", key, err.Error()))
			return unchanged, tagsWritten, errors.New("failed to remove tag")
		}

		setCmd := exec.Command("metaflac", "--set-tag", fmt.Sprintf("%s=%s", key, value), filePath)
		setCmd.Env = utf8Env
		if err := setCmd.Run(); err != nil {
			logger.Log.Error(fmt.Sprintf("failed to set tag %s: %s", key, err.Error()))
			return unchanged, tagsWritten, errors.New("failed to set tag")
		} else {
			tagsWritten++
		}
	}

	return unchanged, tagsWritten, nil
}

func SetMP3Tags(filePath string, metadata models.FileTags) (unchanged bool, tagsWritten int, err error) {
	unchanged = false
	tagsWritten = 0

	genreString := ""
	for index, genre := range metadata.Genres {
		if index != 0 {
			genreString += ";"
		}
		genreString += genre
	}

	desired := map[string]string{
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
		"MusicBrainz Track Id":              metadata.MBRecordingID,
	}

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
	if _, ok := changes["MUSICBRAINZ TRACK ID"]; ok {
		logger.Log.Trace("adding MusicBrainz Track Id")
		addMeta("MusicBrainz Track Id", desired["MusicBrainz Track Id"])
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
	if err := os.Rename(tempOutput, filePath); err != nil {
		return false, 0, fmt.Errorf("failed to replace original file: %w", err)
	}

	return false, tagsWritten, nil
}

func ProcessTrackFile(filePath string, lidarrClient *LidarrClient, plexClient *PlexClient, albumsWhoNeedMetadataRefreshSoFar map[string]string, rootDir string, configFile models.ConfigStruct) (unchanged bool, tagsWritten int, albumsWhoNeedMetadataRefresh map[string]string, err error) {
	unchanged = false
	tagsWritten = 0
	albumsWhoNeedMetadataRefresh = albumsWhoNeedMetadataRefreshSoFar

	// get MB release data from track
	mbReleaseID, err := ExtractMusicBrainzReleaseID(filePath)
	if err != nil {
		logger.Log.Error("failed to extract MB release ID. error: " + err.Error())
		return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, errors.New("failed to extract MB release ID")
	}
	logger.Log.Debug("MB release ID: " + mbReleaseID)

	// get MB data from track
	mbTrackID, err := ExtractMusicBrainzTrackID(filePath)
	if err != nil {
		logger.Log.Error("failed to extract track MB ID. error: " + err.Error())
		return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, errors.New("failed to extract track MB ID")
	}
	logger.Log.Debug("MB track ID: " + mbTrackID)

	// get MB data from track
	trackTitle, err := ExtractTrackTitle(filePath)
	if err != nil {
		logger.Log.Error("failed to extract track title. error: " + err.Error())
		return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, errors.New("failed to extract track title")
	}
	logger.Log.Debug("track title: " + trackTitle)

	// get MB data from track
	mbRecordingID, err := ExtractMusicBrainzRecordingID(filePath)
	if err != nil {
		logger.Log.Error("failed to extract recording MB ID. error: " + err.Error())
		return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, errors.New("failed to extract recording MB ID")
	}
	logger.Log.Debug("MB recording ID: " + mbRecordingID)

	if (mbTrackID == "" || mbReleaseID == "") && lidarrClient != nil {
		logger.Log.Debug("MB track or release ID field empty. trying Lidarr...")
		mbReleaseID, mbTrackID, err = ResolveMBReleaseAndTrackIDFromLidarr(lidarrClient, filePath, rootDir)
		if err != nil {
			logger.Log.Error("failed to retrieve track MB ID from Lidarr. error: " + err.Error())
			return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, errors.New("failed to retrieve track MB ID from Lidarr")
		} else {
			logger.Log.Debug("Lidarr fallback successful")
		}

		logger.Log.Trace("MB release ID: " + mbReleaseID)
		logger.Log.Trace("MB track ID: " + mbTrackID)
	}

	if mbTrackID == "" || mbReleaseID == "" {
		return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, errors.New("MB track or release ID field empty")
	}

	// Get MB data from API
	response, err := GetMusicBrainzRelease(mbReleaseID)
	if err != nil {
		logger.Log.Error("failed to get MB release data. error: " + err.Error())
		return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, errors.New("failed to get MB release data")
	}
	logger.Log.Debug("MB title response: " + response.Title)

	// Go through API response for information
	for mediaCount, media := range response.Media {
		for _, track := range media.Tracks {
			if track.ID == mbTrackID || track.Recording.ID == mbRecordingID || strings.EqualFold(track.Title, trackTitle) {
				if track.ID == mbTrackID {
					logger.Log.Debug("release track ID found in MB response")
				} else if track.ID != mbTrackID && strings.EqualFold(track.Title, trackTitle) {
					logger.Log.Debug("release track ID not found in MB response, but titles match")
				} else if track.ID != mbTrackID && track.Recording.ID == mbRecordingID {
					logger.Log.Debug("release track ID not found in MB response, but recording was")
				}

				trackArtist := ""
				if !configFile.AutotaggerrIgnoreRedundantContributingArtists || len(track.ArtistCredit) > 1 {
					trackArtist = MusicBrainzArtistsArrayToString(track.ArtistCredit, configFile) // change the array into string to be tagged
				}
				logger.Log.Trace("track artists: " + trackArtist)

				trackArtistSemiColon := ""
				for index, artistCredit := range track.ArtistCredit {
					if index != 0 {
						trackArtistSemiColon += "; "
					}
					trackArtistSemiColon += artistCredit.Artist.Name
				}

				// determine release artist
				releaseArtist := ""
				if releaseArtist == "" && len(response.ArtistCredit) > 0 {
					if configFile.AutotaggerrUseCurrentArtistName {
						// use current artist name if configured
						releaseArtist = response.ArtistCredit[0].Artist.Name
					} else {
						// use original release artist name if configured
						releaseArtist = response.ArtistCredit[0].Name
					}
				} else if releaseArtist == "" {
					return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, errors.New("failed to determine album artist")
				}

				releaseArtistID := ""
				for index, artist := range track.ArtistCredit {
					if index != 0 {
						releaseArtistID += "; "
					}
					releaseArtistID += artist.Artist.ID
				}

				releaseGroupArtistID := ""
				for index, artist := range response.ArtistCredit {
					if index != 0 {
						releaseGroupArtistID += "; "
					}
					releaseGroupArtistID += artist.Artist.ID
				}

				releaseTime, err := MusicBrainzDateStringToDateTime(response.Date)
				releaseYear := ""
				releaseDate := ""
				if err == nil {
					releaseYear = strconv.Itoa(releaseTime.Year())
					releaseDate = releaseTime.Format("2006-01-02")
				}

				releaseGroupTime, err := MusicBrainzDateStringToDateTime(response.ReleaseGroup.FirstReleaseDate)
				releaseGroupYear := ""
				releaseGroupDate := ""
				if err == nil {
					releaseGroupYear = strconv.Itoa(releaseGroupTime.Year())
					releaseGroupDate = releaseGroupTime.Format("2006-01-02")
				}

				isrcString := ""
				for index, isrc := range track.Recording.ISRCs {
					if index != 0 {
						isrcString += "; "
					}
					isrcString += isrc
				}

				recordLabelString := ""
				if len(response.LabelInfo) > 0 {
					for index, recordLabel := range response.LabelInfo {
						if index != 0 {
							recordLabelString += "; "
						}
						recordLabelString += recordLabel.Label.Name
					}
				}

				metadata := models.FileTags{
					Artist:                trackArtist,
					ArtistSemicolon:       trackArtistSemiColon,
					AlbumArtist:           releaseArtist,
					OriginalDate:          releaseGroupDate,
					OriginalYear:          releaseGroupYear,
					ReleaseDate:           releaseDate,
					ReleaseYear:           releaseYear,
					Album:                 response.Title,
					Title:                 track.Title,
					ISRC:                  isrcString,
					Track:                 track.Number,
					TrackTotal:            strconv.Itoa(len(media.Tracks)),
					DiscNumber:            strconv.Itoa(mediaCount + 1),
					DiscTotal:             strconv.Itoa(len(response.Media)),
					MBAlbumStatus:         strings.ToLower(response.Status),
					MBAlbumType:           ReleaseToAlbumType(response),
					MBAlbumReleaseCountry: response.Country,
					MBAlbumID:             mbReleaseID,
					MBArtistID:            releaseArtistID,
					MBAlbumArtistID:       releaseGroupArtistID,
					MBReleaseGroupID:      response.ReleaseGroup.ID,
					MBReleaseTrackID:      track.ID,
					MBRecordingID:         track.Recording.ID,
					Script:                response.TextRepresentation.Script,
					RecordLabel:           recordLabelString,
					Media:                 media.Format,
					Barcode:               response.Barcode,
				}

				for _, genre := range response.ReleaseGroup.Genres {
					metadata.Genres = append(metadata.Genres, genre.Name)
				}

				// re-tag file with new information
				unchanged, tagsWritten, err := SetFileTags(filePath, metadata, configFile)
				if err != nil {
					logger.Log.Error("failed to set file tags. error: " + err.Error())
					return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, errors.New("failed to set FLAC artist tags")
				} else {
					logger.Log.Debug("file tagger finished")
				}

				changeString := "unchanged"
				if !unchanged {
					changeString = "changed. tags written: " + strconv.Itoa(tagsWritten)
				}

				if plexClient != nil && !unchanged {
					albumsWhoNeedMetadataRefresh, err = PlexRefreshForFile(unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, *plexClient, response.Title, releaseArtist, track.Title)
					if err != nil {
						logger.Log.Warn("failed to prepare Plex refresh for album. error: " + err.Error())
					}
				}

				logger.Log.Debug("file processed. " + changeString + ". path: '" + filePath + "'")
				return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, nil
			}
		}
	}

	return unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, errors.New("failed to tag file, track not found in release data")
}

func ScanFolderRecursive(root string, lidarrClient *LidarrClient, plexClient *PlexClient, albumsWhoNeedMetadataRefreshSoFar map[string]string, configFile models.ConfigStruct) (
	counter int,
	unchangedFiles int,
	allTagsWritten int,
	errorFiles []string,
	albumsWhoNeedMetadataRefresh map[string]string,
	err error,
) {
	originalRoot := root
	counter = 0
	unchangedFiles = 0
	allTagsWritten = 0
	errorFiles = []string{}

	// load cache into memory
	err = MusicbrainzLoadCache()
	if err != nil {
		logger.Log.Error("failed to load release cache. error: " + err.Error())
		return counter, unchangedFiles, allTagsWritten, errorFiles, albumsWhoNeedMetadataRefreshSoFar, errors.New("failed to load release cache")
	}

	// first pass, count total supported files
	totalFiles := 0
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && supportedExtensions[strings.ToLower(filepath.Ext(path))] {
			totalFiles++
		}
		return nil
	})

	if totalFiles == 0 {
		logger.Log.Info("no supported files found in: " + root)
		return counter, unchangedFiles, allTagsWritten, errorFiles, albumsWhoNeedMetadataRefreshSoFar, nil
	}

	logger.Log.Info(fmt.Sprintf("found %d supported files. starting processing...", totalFiles))

	// track progress thresholds (10%, 20%, ... 100%)
	nextProgress := 10

	albumsWhoNeedMetadataRefresh = albumsWhoNeedMetadataRefreshSoFar

	// second pass, actual processing
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		unchanged := false
		tagsWritten := 0

		if supportedExtensions[strings.ToLower(filepath.Ext(path))] {
			unchanged, tagsWritten, albumsWhoNeedMetadataRefresh, err = ProcessTrackFile(path, lidarrClient, plexClient, albumsWhoNeedMetadataRefresh, originalRoot, configFile)
			if err != nil {
				logger.Log.Error("failed to process file '" + path + "'. error: " + err.Error())
				errorFiles = append(errorFiles, path)
			} else {
				counter++
				if unchanged {
					unchangedFiles++
				}
				allTagsWritten += tagsWritten

				// print intervals
				progress := (counter * 100) / totalFiles
				if progress >= nextProgress {
					logger.Log.Info(fmt.Sprintf("progress: %d%% (%d/%d files)", progress, counter, totalFiles))
					nextProgress += 10
				}
			}
		}
		return nil
	})

	return counter, unchangedFiles, allTagsWritten, errorFiles, albumsWhoNeedMetadataRefresh, err
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
			res["MUSICBRAINZ ALBUM TYPE"] = append(res["MUSICBRAINZ ALBUM TYPE"], val)
		case "musicbrainz album release country":
			res[strings.ToUpper(key)] = append(res[strings.ToUpper(key)], val)
		case "musicbrainz album status":
			res[strings.ToUpper(key)] = append(res[strings.ToUpper(key)], val)
		case "musicbrainz album id":
			res[strings.ToUpper(key)] = append(res[strings.ToUpper(key)], val)
		case "musicbrainz artist id":
			res[strings.ToUpper(key)] = append(res[strings.ToUpper(key)], val)
		case "musicbrainz track id":
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

// try to retrieve the MB release from Lidarr
func ResolveMBReleaseAndTrackIDFromLidarr(cli *LidarrClient, trackPath string, rootDir string) (string, string, error) {
	mbTrackID := ""
	mbReleaseID := ""

	// derive the artist from the path folder
	artistName, err := utilities.ExtractArtistNameFromTrackFilePath(rootDir, trackPath)
	if err != nil {
		return "", "", err
	}

	artist, err := cli.FindArtistByName(artistName)
	if err != nil {
		return "", "", err
	}

	tf, err := cli.FindTrackFileByPath(artist.ID, trackPath, rootDir)
	if err != nil {
		return "", "", err
	}

	logger.Log.Trace("Lidarr track file: ")
	logger.Log.Trace(tf)

	tracks, err := cli.GetTracksByAlbumAndArtistID(artist.ID, tf.AlbumID)
	if err != nil {
		return "", "", err
	}

	for _, track := range tracks {
		if track.TrackFileID == tf.ID {
			mbTrackID = track.ForeignTrackID
		}
	}

	mbReleaseID, err = cli.GetMonitoredAlbumMBID(artist.ID, tf.AlbumID)
	if err != nil {
		return "", "", err
	}

	return mbReleaseID, mbTrackID, nil
}

func PlexRefreshForFile(unchanged bool, tagsWritten int, albumsWhoNeedMetadataRefreshInput map[string]string, plexClient PlexClient, albumTitle string, releaseArtist string, trackTitle string) (albumsWhoNeedMetadataRefresh map[string]string, err error) {
	albumsWhoNeedMetadataRefresh = albumsWhoNeedMetadataRefreshInput

	err = PlexLoadAlbumKeyCache()
	if err != nil {
		return albumsWhoNeedMetadataRefresh, err
	}

	albumKey := ""
	if cached, ok := plexAlbumKeyCache[albumTitle]; ok {
		logger.Log.Trace("cached entry found")
		if time.Since(cached.Timestamp) < plexAlbumKeyCacheDuration {
			logger.Log.Debug("returning cached album key for album: " + albumTitle)
			albumKey = cached.AlbumKey
		}
	} else {
		sectionID, err := plexClient.FindMusicSectionID()
		if err != nil {
			logger.Log.Error("failed to find Plex music section ID. error: " + err.Error())
			return albumsWhoNeedMetadataRefresh, errors.New("failed to find Plex music section ID")
		}

		artistKey, err := plexClient.FindArtistKey(sectionID, releaseArtist)
		if err != nil {
			logger.Log.Error("failed to find Plex artist key for '" + releaseArtist + "'. error: " + err.Error())
			return albumsWhoNeedMetadataRefresh, errors.New("failed to find Plex artist key for '" + releaseArtist + "'")
		}

		logger.Log.Trace(artistKey + " - " + albumTitle)

		albumKey, err := plexClient.ResolveAlbumKeyInSection(sectionID, releaseArtist, albumTitle, trackTitle)
		if err != nil {
			logger.Log.Error("failed to find Plex album key. error: " + err.Error())
			return albumsWhoNeedMetadataRefresh, errors.New("failed to find Plex album key")
		} else {
			logger.Log.Trace(albumKey)
		}

		// add album key to cache
		plexAlbumKeyCache[albumTitle] = models.PlexAlbumKeyCache{
			AlbumKey:  albumKey,
			Timestamp: time.Now(),
		}

		// save new cache
		err = PlexSaveAlbumKeyCache()
		if err != nil {
			return albumsWhoNeedMetadataRefresh, err
		}
	}

	if !unchanged && tagsWritten > 0 {
		albumsWhoNeedMetadataRefresh[albumTitle] = albumKey
	}

	return
}

func ReleaseToAlbumType(release models.MusicBrainzReleaseResponse) string {
	if strings.Contains(strings.ToLower(release.Title), "remix") || strings.Contains(strings.ToLower(release.Disambiguation), "remix") {
		return "album; remix"
	}

	if len(release.ReleaseGroup.SecondaryTypes) == 0 {
		return strings.ToLower(release.ReleaseGroup.PrimaryType)
	}

	primarySecondary := release.ReleaseGroup.SecondaryTypes[0]
	switch strings.ToLower(primarySecondary) {
	case "soundtrack":
		return "album; soundtrack"
	case "compilation":
		return "album; compilation"
	case "remix":
		return "album; remix"
	case "live":
		return "album; live"
	case "demo":
		return "album; demo"
	}

	switch strings.ToLower(release.ReleaseGroup.PrimaryType) {
	case "album":
		return "album"
	case "ep":
		return "ep"
	case "single":
		return "single"
	}

	return ""
}
