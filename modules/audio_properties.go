package modules

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aunefyren/autotaggerr/quality"
	"github.com/mewkiz/flac"
)

// AudioProperties is what a file's audio is: what the naming tokens {MediaInfo
// AudioCodec} and {MediaInfo AudioBitsPerSample} render, and what its quality is
// measured from (Quality).
type AudioProperties struct {
	// Codec is Lidarr's name for it: "FLAC", "MP3".
	Codec string
	// BitsPerSample is the sample depth of lossless audio (16, 24). Zero for lossy
	// audio, which has none: an MP3 is decoded to whatever depth the player picks.
	BitsPerSample int
	// SampleRate in Hz; zero when the headers did not say.
	SampleRate int
	// BitrateKbps is an MP3's bitrate: the frame header's for CBR, the average the
	// Xing/VBRI header gives for VBR. Zero for FLAC, whose bitrate says nothing about
	// its quality, and for a VBR file whose header gives no totals.
	BitrateKbps int
	// VBR is an MP3 whose first frame carries a Xing or VBRI header saying so.
	VBR bool
	// VBRQuality is the LAME preset a VBR file was encoded with (0 for V0 … 9), read
	// from the Xing header's quality field; -1 when the file does not say.
	VBRQuality int
}

// Quality places the audio on the quality ladder (see the quality package).
func (a AudioProperties) Quality() quality.ID {
	return quality.Classify(quality.Measured{
		Codec:       a.Codec,
		BitrateKbps: a.BitrateKbps,
		VBR:         a.VBR,
		VBRQuality:  a.VBRQuality,
		BitDepth:    a.BitsPerSample,
		SampleRate:  a.SampleRate,
	})
}

// ErrAudioUnreadable is a file whose headers do not say what its audio is.
var ErrAudioUnreadable = errors.New("could not read what the file's audio is")

// ReadAudioProperties reads a file's codec, sample depth, rate and bitrate from its
// headers alone — FLAC's STREAMINFO, an MP3's first frame and the Xing/VBRI header in
// it — never the audio. Read from the file rather than told by its extension, so a
// misnamed file is named for what it is. Organise reads this for every file it plans,
// and Process for every file whose quality is not yet measured, so it has to stay a
// header read.
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
		return AudioProperties{
			Codec:         "FLAC",
			BitsPerSample: int(stream.Info.BitsPerSample),
			SampleRate:    int(stream.Info.SampleRate),
			VBRQuality:    -1,
		}, nil
	case ".mp3":
		props, ok := mp3Properties(filePath)
		if !ok {
			return AudioProperties{}, fmt.Errorf("%w: no MPEG audio frame", ErrAudioUnreadable)
		}
		return props, nil
	}
	return AudioProperties{}, fmt.Errorf("%w: unsupported file type", ErrAudioUnreadable)
}

// mp3HeaderWindow is how much of the first frame is read: enough for the side info,
// a Xing header with its 100-byte TOC, and its quality field.
const mp3HeaderWindow = 512

// mp3Properties finds the first Layer III frame after the tags, within the distance
// mp3Duration searches for one, and reads it and any Xing ("Xing" for VBR, "Info" for
// CBR) or VBRI header it carries.
func mp3Properties(filePath string) (AudioProperties, bool) {
	file, err := os.Open(filePath)
	if err != nil {
		return AudioProperties{}, false
	}
	defer file.Close()
	start, err := audioPayloadStart(file)
	if err != nil {
		return AudioProperties{}, false
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return AudioProperties{}, false
	}
	reader := bufio.NewReaderSize(file, 64*1024)
	for skipped := 0; skipped <= mp3SyncSearchLimit; skipped++ {
		header, err := reader.Peek(4)
		if err != nil {
			return AudioProperties{}, false
		}
		if _, samples, rate, err := mpegFrame(header); err == nil {
			frame, _ := reader.Peek(mp3HeaderWindow) // short near EOF: what there is
			return mp3FrameProperties(frame, samples, rate), true
		}
		if _, err := reader.Discard(1); err != nil {
			return AudioProperties{}, false
		}
	}
	return AudioProperties{}, false
}

// mp3FrameProperties reads a frame already known to be a valid Layer III header.
func mp3FrameProperties(frame []byte, samples, rate int) AudioProperties {
	props := AudioProperties{Codec: "MP3", SampleRate: rate, VBRQuality: -1}
	version := (frame[1] >> 3) & 0x03
	if version == 3 {
		props.BitrateKbps = mpeg1L3Bitrates[frame[2]>>4]
	} else {
		props.BitrateKbps = mpeg2L3Bitrates[frame[2]>>4]
	}
	mono := frame[3]>>6 == 3
	sideInfo := 32
	switch {
	case version == 3 && mono:
		sideInfo = 17
	case version != 3 && !mono:
		sideInfo = 17
	case version != 3:
		sideInfo = 9
	}

	avg := func(frames, bytes uint32) int {
		if frames == 0 || bytes == 0 || rate == 0 {
			return 0
		}
		seconds := float64(frames) * float64(samples) / float64(rate)
		return int(float64(bytes)*8/seconds/1000 + 0.5)
	}

	if x := 4 + sideInfo; len(frame) >= x+8 {
		tag := string(frame[x : x+4])
		if tag == "Xing" || tag == "Info" {
			flags := binary.BigEndian.Uint32(frame[x+4:])
			pos := x + 8
			var frames, bytes uint32
			if flags&0x1 != 0 && len(frame) >= pos+4 {
				frames = binary.BigEndian.Uint32(frame[pos:])
				pos += 4
			}
			if flags&0x2 != 0 && len(frame) >= pos+4 {
				bytes = binary.BigEndian.Uint32(frame[pos:])
				pos += 4
			}
			if flags&0x4 != 0 {
				pos += 100
			}
			if tag == "Xing" {
				props.VBR = true
				props.BitrateKbps = avg(frames, bytes)
				// LAME writes 100 - 10*V - q here, q being its 0–9 algorithm quality,
				// so the tens digit gives the preset however q was set.
				if flags&0x8 != 0 && len(frame) >= pos+4 {
					if q := binary.BigEndian.Uint32(frame[pos:]); q > 0 && q <= 100 {
						props.VBRQuality = int(100-q) / 10
					}
				}
			}
			return props
		}
	}
	// Fraunhofer's VBRI sits 32 bytes after the frame header whatever the mode.
	if x := 4 + 32; len(frame) >= x+18 && string(frame[x:x+4]) == "VBRI" {
		props.VBR = true
		props.BitrateKbps = avg(binary.BigEndian.Uint32(frame[x+14:]), binary.BigEndian.Uint32(frame[x+10:]))
	}
	return props
}
