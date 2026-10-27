package modules

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mewkiz/flac"
)

// AudioProperties is what a file's audio is, as the naming tokens {MediaInfo
// AudioCodec} and {MediaInfo AudioBitsPerSample} render it.
type AudioProperties struct {
	// Codec is Lidarr's name for it: "FLAC", "MP3".
	Codec string
	// BitsPerSample is the sample depth of lossless audio (16, 24). Zero for lossy
	// audio, which has none: an MP3 is decoded to whatever depth the player picks.
	BitsPerSample int
}

// ErrAudioUnreadable is a file whose headers do not say what its audio is.
var ErrAudioUnreadable = errors.New("could not read what the file's audio is")

// ReadAudioProperties reads a file's codec and sample depth from its headers alone —
// FLAC's STREAMINFO, an MP3's first frame header — never the audio. Read from the file
// rather than told by its extension, so a misnamed file is named for what it is.
// Organise reads this for every file it plans, so it has to stay a header read.
func ReadAudioProperties(filePath string) (AudioProperties, error) {
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".flac":
		stream, err := flac.Open(filePath)
		if err != nil {
			return AudioProperties{}, fmt.Errorf("%w: %s", ErrAudioUnreadable, err.Error())
		}
		defer stream.Close()
		if stream.Info == nil || stream.Info.BitsPerSample == 0 {
			return AudioProperties{}, fmt.Errorf("%w: no STREAMINFO", ErrAudioUnreadable)
		}
		return AudioProperties{Codec: "FLAC", BitsPerSample: int(stream.Info.BitsPerSample)}, nil
	case ".mp3":
		if !hasMPEGFrame(filePath) {
			return AudioProperties{}, fmt.Errorf("%w: no MPEG audio frame", ErrAudioUnreadable)
		}
		return AudioProperties{Codec: "MP3"}, nil
	}
	return AudioProperties{}, fmt.Errorf("%w: unsupported file type", ErrAudioUnreadable)
}

// hasMPEGFrame reports whether a Layer III frame header follows the file's tags, within
// the distance mp3Duration searches for one.
func hasMPEGFrame(filePath string) bool {
	file, err := os.Open(filePath)
	if err != nil {
		return false
	}
	defer file.Close()
	start, err := audioPayloadStart(file)
	if err != nil {
		return false
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return false
	}
	reader := bufio.NewReaderSize(file, 64*1024)
	for skipped := 0; skipped <= mp3SyncSearchLimit; skipped++ {
		header, err := reader.Peek(4)
		if err != nil {
			return false
		}
		if _, _, _, err := mpegFrame(header); err == nil {
			return true
		}
		if _, err := reader.Discard(1); err != nil {
			return false
		}
	}
	return false
}
