package controllers

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/bogem/id3v2"
	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/meta"
)

// extractMusicBrainzReleaseID extracts the MusicBrainz Album ID from either MP3 (ID3v2) or FLAC (Vorbis)
func extractMusicBrainzReleaseID(filePath string) (string, error) {
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".mp3":
		return extractFromID3v2(filePath)
	case ".flac":
		return extractFromFLAC(filePath)
	default:
		return "", errors.New("unsupported file type")
	}
}

func extractFromID3v2(filePath string) (string, error) {
	tagFile, err := id3v2.Open(filePath, id3v2.Options{Parse: true})
	if err != nil {
		return "", err
	}
	defer tagFile.Close()

	for _, frame := range tagFile.GetFrames("TXXX") {
		userFrame, ok := frame.(id3v2.UserDefinedTextFrame)
		if !ok {
			continue
		}

		if userFrame.Description == "MusicBrainz Album Id" || userFrame.Description == "MusicBrainz Release Id" {
			return userFrame.Value, nil
		}
	}
	return "", nil
}

func extractFromFLAC(filePath string) (string, error) {
	stream, err := flac.ParseFile(filePath)
	if err != nil {
		return "", err
	}

	for _, block := range stream.Blocks {
		if commentBlock, ok := block.Body.(*meta.VorbisComment); ok {
			for _, tag := range commentBlock.Tags {
				key := strings.ToUpper(tag[0])
				if key == "MUSICBRAINZ_ALBUMID" {
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
