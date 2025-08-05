package modules

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
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
		return extractFromFLAC(filePath, "release")
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
		return extractFromFLAC(filePath, "track")
	default:
		return "", errors.New("unsupported file type")
	}
}

func extractFromID3v2(filePath string, metadataType string) (string, error) {
	keyName := ""
	tagFile, err := id3v2.Open(filePath, id3v2.Options{Parse: true})
	if err != nil {
		return "", err
	}
	defer tagFile.Close()

	switch metadataType {
	case "recording":
		keyName = ""
	case "release":
		keyName = "MusicBrainz Release Id"
	case "release_group":
		keyName = ""
	case "track":
		keyName = "MusicBrainz Track Id"
	case "artist":
		keyName = ""
	default:
		return "", errors.New("unsupported media type")
	}

	frames := tagFile.GetFrames("TXXX")
	logger.Log.Trace(fmt.Sprintf("mp3 frames found: %s", frames))

	for _, frame := range frames {
		userFrame, ok := frame.(id3v2.UserDefinedTextFrame)
		if !ok {
			continue
		}

		desc := strings.TrimSpace(strings.ToLower(userFrame.Description))
		logger.Log.Trace(fmt.Sprintf("mp3 frame name: %s, value: %s", desc, userFrame.Value))
		if desc == "txxx:"+strings.TrimSpace(strings.ToLower(keyName)) {
			return userFrame.Value, nil
		}
	}
	return "", nil
}

func extractFromFLAC(filePath string, metadataType string) (string, error) {
	keyName := ""
	stream, err := flac.ParseFile(filePath)
	if err != nil {
		return "", err
	}

	switch metadataType {
	case "track":
		keyName = "MUSICBRAINZ_RELEASETRACKID"
	case "release":
		keyName = "MUSICBRAINZ_ALBUMID"
	case "release_group":
		keyName = "MUSICBRAINZ_RELEASEGROUPID"
	case "recording":
		keyName = "MUSICBRAINZ_TRACKID"
	case "artist":
		keyName = "MUSICBRAINZ_ALBUMARTISTID"
	default:
		return "", errors.New("unsupported media type")
	}

	for _, block := range stream.Blocks {
		if commentBlock, ok := block.Body.(*meta.VorbisComment); ok {
			for _, tag := range commentBlock.Tags {
				key := strings.ToUpper(tag[0])
				if key == keyName {
					return tag[1], nil
				}
			}
		}
	}

	return "", nil // Not found
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

/*
// writeMusicBrainzAlbumIDToFLAC updates or adds MUSICBRAINZ_ALBUMID in a FLAC file.
func writeMusicBrainzAlbumIDToFLAC(filePath string, mbid string) error {
	// Parse the FLAC file
	stream, err := flac.ParseFile(filePath)
	if err != nil {
		return err
	}

	found := false

	// Search and modify the VorbisComment block
	for _, block := range stream.Blocks {
		if block.Type == meta.TypeVorbisComment {
			if commentBlock, ok := block.Body.(*meta.VorbisComment); ok {
				for i, tag := range commentBlock.Tags {
					if tag[0] == "MUSICBRAINZ_ALBUMID" {
						commentBlock.Tags[i][1] = mbid
						found = true
						break
					}
				}
				if !found {
					commentBlock.Tags = append(commentBlock.Tags, [2]string{"MUSICBRAINZ_ALBUMID", mbid})
					found = true
				}
			}
			break
		}
	}

	// If VorbisComment block not found, create and append one
	if !found {
		newBlockm, err := meta.New(meta.TypeVorbisComment)
		if commentBlock, ok := newBlock.Body.(*meta.VorbisComment); ok {
			commentBlock.Tags = append(commentBlock.Tags, [2]string{"MUSICBRAINZ_ALBUMID", mbid})
		}
		stream.Meta = append(stream.Meta, newBlock)
	}

	// Write to temp file
	tmpPath := filePath + ".tmp"
	out, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	defer out.Close()

	if err := stream.Write(out); err != nil {
		return err
	}

	// Replace original file
	if err := os.Rename(tmpPath, filePath); err != nil {
		return err
	}

	return nil
}
*/

func SetFileTags(filePath string, metadata models.FileTags) error {
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".mp3":
		return SetMP3Tags(filePath, metadata)
	case ".flac":
		return SetFlacTags(filePath, metadata)
	default:
		return errors.New("unsupported file type")
	}
}

// SetFlacTags updates multiple Vorbis comment tags on a FLAC file.
func SetFlacTags(filePath string, metadata models.FileTags) error {
	tags := map[string]string{
		"ARTIST":      metadata.Artist,
		"ALBUMARTIST": metadata.AlbumArtist,
		"GENRE":       metadata.Genre,
		"DATE":        metadata.Date,
		"YEAR":        metadata.Year,
		"ALBUM":       metadata.Album,
		"TITLE":       metadata.Title,
		"TRACKNUMBER": metadata.Track,
		"TRACKTOTAL":  metadata.TrackTotal,
		"DISCNUMBER":  metadata.DiscNumber,
		"DISCTOTAL":   metadata.DiscTotal,
		"ISRC":        metadata.ISRC,
	}

	logger.Log.Debug("Artist tag added: %q\n", metadata.Artist)

	for key, value := range tags {
		if value == "" {
			continue // Skip empty fields
		}

		// Construct environment with UTF-8 support
		utf8Env := append(os.Environ(), "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8")

		// Remove existing tag
		removeCmd := exec.Command("metaflac", "--remove-tag="+key, filePath)
		removeCmd.Env = utf8Env
		if err := removeCmd.Run(); err != nil {
			logger.Log.Error(fmt.Sprintf("failed to remove tag %s: %s", key, err.Error()))
			return errors.New("failed to remove tag")
		}

		// Set new tag
		setCmd := exec.Command("metaflac", "--set-tag", fmt.Sprintf("%s=%s", key, value), filePath)
		setCmd.Env = utf8Env
		if err := setCmd.Run(); err != nil {
			logger.Log.Error(fmt.Sprintf("failed to set tag %s: %s", key, err.Error()))
			return errors.New("failed to set tag")
		}
	}

	return nil
}

func SetMP3Tags(filePath string, metadata models.FileTags) error {
	// Generate a temporary output path
	tempOutput := filePath + ".temp.mp3"

	// Construct ffmpeg command
	args := []string{
		"-i", filePath,
		"-y",                 // Overwrite output
		"-map_metadata", "0", // Copy existing metadata as base
		"-codec", "copy", // Don't re-encode audio
	}

	// Add standard metadata fields
	if metadata.Artist != "" {
		args = append(args, "-metadata", fmt.Sprintf("artist=%s", metadata.Artist))
	}
	if metadata.AlbumArtist != "" {
		args = append(args, "-metadata", fmt.Sprintf("album_artist=%s", metadata.AlbumArtist))
	}
	if metadata.Genre != "" {
		args = append(args, "-metadata", fmt.Sprintf("genre=%s", metadata.Genre))
	}
	if metadata.Year != "" {
		args = append(args, "-metadata", fmt.Sprintf("year=%s", metadata.Year))
	}
	if metadata.Date != "" {
		args = append(args, "-metadata", fmt.Sprintf("date=%s", metadata.Date))
	}
	if metadata.Album != "" {
		args = append(args, "-metadata", fmt.Sprintf("album=%s", metadata.Album))
	}
	if metadata.Title != "" {
		args = append(args, "-metadata", fmt.Sprintf("title=%s", metadata.Title))
	}
	if metadata.Track != "" && metadata.TrackTotal != "" {
		args = append(args, "-metadata", fmt.Sprintf("track=%s/%s", metadata.Track, metadata.TrackTotal))
	} else if metadata.Track != "" {
		args = append(args, "-metadata", fmt.Sprintf("track=%s", metadata.Track))
	}
	if metadata.DiscNumber != "" && metadata.DiscTotal != "" {
		args = append(args, "-metadata", fmt.Sprintf("disc=%s/%s", metadata.DiscNumber, metadata.DiscTotal))
	} else if metadata.DiscNumber != "" {
		args = append(args, "-metadata", fmt.Sprintf("disc=%s", metadata.DiscNumber))
	}

	// Add custom tags using TXXX
	if metadata.ISRC != "" {
		args = append(args, "-metadata", fmt.Sprintf("TXXX=ISRC:%s", metadata.ISRC))
	}
	if metadata.TrackTotal != "" {
		args = append(args, "-metadata", fmt.Sprintf("TXXX=TRACKTOTAL:%s", metadata.TrackTotal))
	}
	if metadata.DiscTotal != "" {
		args = append(args, "-metadata", fmt.Sprintf("TXXX=DISCTOTAL:%s", metadata.DiscTotal))
	}

	args = append(args, tempOutput)

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stderr = os.Stderr // for debugging
	cmd.Stdout = os.Stdout

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg tagging failed: %w", err)
	}

	// Replace original file with the new one
	if err := os.Rename(tempOutput, filePath); err != nil {
		return fmt.Errorf("failed to replace original file: %w", err)
	}

	return nil
}

func ProcessTrackFile(filePath string) error {
	mbReleaseID, err := ExtractMusicBrainzReleaseID(filePath)
	if err != nil {
		logger.Log.Error("failed to extract MB release ID. error: " + err.Error())
		return errors.New("failed to extract MB release ID")
	}
	logger.Log.Debug("MB release ID: " + mbReleaseID)

	// Get MB data from track
	mbTrackID, err := ExtractMusicBrainzTrackID(filePath)
	if err != nil {
		logger.Log.Error("failed to extract track MB ID. error: " + err.Error())
		return errors.New("failed to extract track MB ID")
	}
	logger.Log.Debug("MB track ID: " + mbTrackID)

	if mbTrackID == "" || mbReleaseID == "" {
		return errors.New("MB track or release ID field empty")
	}

	// Get MB data from API
	response, err := GetMusicBrainzRelease(mbReleaseID)
	if err != nil {
		logger.Log.Error("failed to get MB release data. error: " + err.Error())
		return errors.New("failed to get MB release data")
	}
	logger.Log.Debug("MB title response: " + response.Title)

	// Go through API response for information
	for mediaCount, media := range response.Media {
		for _, track := range media.Tracks {
			if track.ID == mbTrackID {
				logger.Log.Debug("Release track ID found in MB response")
				trackArtist := MusicBrainzArtistsArrayToString(track.ArtistCredit)
				logger.Log.Debug(trackArtist)

				releaseArtist := MusicBrainzArtistsArrayToString(response.ArtistCredit)
				releaseTime, err := MusicBrainzDateStringToDateTime(response.Date)
				releaseYear := ""
				releaseDate := ""
				isrc := ""
				if err == nil {
					releaseYear = strconv.Itoa(releaseTime.Year())
					releaseDate = releaseTime.Format("2006-01-02")
				}
				if len(track.Recording.ISRCs) > 0 {
					isrc = track.Recording.ISRCs[0]
				}

				metadata := models.FileTags{
					Artist:      trackArtist,
					AlbumArtist: releaseArtist,
					Genre:       "",
					Date:        releaseDate,
					Year:        releaseYear,
					Album:       response.Title,
					Title:       track.Title,
					ISRC:        isrc,
					Track:       track.Number,
					TrackTotal:  strconv.Itoa(len(media.Tracks)),
					DiscNumber:  strconv.Itoa(mediaCount + 1),
					DiscTotal:   strconv.Itoa(len(response.Media)),
				}

				// re-tag file with new information
				err = SetFileTags(filePath, metadata)
				if err != nil {
					logger.Log.Error("failed to set FLAC artist tags. error: " + err.Error())
					return errors.New("failed to set FLAC artist tags")
				}

				logger.Log.Info("file processed: " + filePath)
				return nil
			}
		}
	}

	return errors.New("failed to tag file, track not found in release data")
}

func ScanFolderRecursive(root string) (int, error) {
	counter := 0
	return counter, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err // report permission errors, etc.
		}
		if d.IsDir() {
			return nil // keep walking
		}
		if supportedExtensions[strings.ToLower(filepath.Ext(path))] {
			err = ProcessTrackFile(path)
			if err != nil {
				logger.Log.Error("failed to process file '" + path + "'. error: " + err.Error())
			} else {
				counter++
			}
		}
		return nil
	})
}
