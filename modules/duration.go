package modules

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mewkiz/flac"
)

// audioDuration returns a file's playing time in seconds, or false when it cannot be
// told. It is the disc guard's second signal (verifyDiscFolder) and nothing else reads
// it, so "cannot tell" is always an acceptable answer: the guard then has the folder
// to go on, as it always had. A var so the guard's tests can state durations directly.
var audioDuration = func(filePath string) (float64, bool) {
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".flac":
		return flacDuration(filePath)
	case ".mp3":
		return mp3Duration(filePath)
	}
	return 0, false
}

// flacDuration reads the length from STREAMINFO, which every FLAC carries first: the
// total sample count over the sample rate, without touching the audio frames.
func flacDuration(filePath string) (float64, bool) {
	stream, err := flac.Open(filePath)
	if err != nil {
		return 0, false
	}
	defer stream.Close()
	if stream.Info == nil || stream.Info.SampleRate == 0 || stream.Info.NSamples == 0 {
		return 0, false // NSamples 0 means "unknown" in STREAMINFO
	}
	return float64(stream.Info.NSamples) / float64(stream.Info.SampleRate), true
}

// mp3Duration counts MPEG audio frames. MP3 has no header stating its length, and a
// VBR file's bitrate says nothing about it either, so walking the frames is the only
// answer that holds for both: each frame header gives the frame's size and how many
// samples it plays. It reads headers, not audio, and stops at the first thing that is
// not a frame (a trailing ID3v1 or APE tag).
//
// Layer III only, which is what an .mp3 is in practice; anything else is "cannot tell".
func mp3Duration(filePath string) (float64, bool) {
	file, err := os.Open(filePath)
	if err != nil {
		return 0, false
	}
	defer file.Close()

	start, err := audioPayloadStart(file)
	if err != nil {
		return 0, false
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return 0, false
	}
	reader := bufio.NewReaderSize(file, 64*1024)

	var seconds float64
	frames, skipped := 0, 0
	for {
		header, err := reader.Peek(4)
		if err != nil {
			break
		}
		size, samples, rate, err := mpegFrame(header)
		if err != nil {
			if frames > 0 {
				break // the audio has ended: a trailing tag, or junk
			}
			// Padding or junk before the first frame. Step one byte and look again,
			// as decoders do — but not through a whole file that is not MPEG audio.
			if skipped++; skipped > mp3SyncSearchLimit {
				break
			}
			if _, err := reader.Discard(1); err != nil {
				break
			}
			continue
		}
		if _, err := reader.Discard(size); err != nil {
			break // a truncated last frame: what was counted stands
		}
		frames++
		seconds += float64(samples) / float64(rate)
	}
	if frames == 0 {
		return 0, false
	}
	return seconds, true
}

var errNotMPEGFrame = errors.New("not an MPEG audio frame header")

// mp3SyncSearchLimit bounds how far past the tags the first frame is looked for.
const mp3SyncSearchLimit = 64 * 1024

// Bitrates (kbit/s) for Layer III, by MPEG-1 and MPEG-2/2.5, indexed by the header's
// bitrate field; 0 is "free format" and 15 is invalid, both refused.
var (
	mpeg1L3Bitrates = [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	mpeg2L3Bitrates = [16]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
	mpegSampleRates = map[byte][3]int{
		3: {44100, 48000, 32000}, // MPEG-1
		2: {22050, 24000, 16000}, // MPEG-2
		0: {11025, 12000, 8000},  // MPEG-2.5
	}
)

// mpegFrame decodes a Layer III frame header: the frame's size in bytes, the samples
// it plays, and its sample rate.
func mpegFrame(h []byte) (size, samples, rate int, err error) {
	if h[0] != 0xFF || h[1]&0xE0 != 0xE0 {
		return 0, 0, 0, errNotMPEGFrame
	}
	version := (h[1] >> 3) & 0x03 // 3 = MPEG-1, 2 = MPEG-2, 0 = MPEG-2.5, 1 reserved
	layer := (h[1] >> 1) & 0x03   // 1 = Layer III
	if version == 1 || layer != 1 {
		return 0, 0, 0, errNotMPEGFrame
	}
	bitrateIndex := h[2] >> 4
	rateIndex := (h[2] >> 2) & 0x03
	padding := int((h[2] >> 1) & 0x01)
	if rateIndex == 3 {
		return 0, 0, 0, errNotMPEGFrame
	}
	rate = mpegSampleRates[version][rateIndex]

	var kbps int
	if version == 3 {
		kbps = mpeg1L3Bitrates[bitrateIndex]
		samples = 1152
	} else {
		kbps = mpeg2L3Bitrates[bitrateIndex]
		samples = 576
	}
	if kbps == 0 {
		return 0, 0, 0, errNotMPEGFrame
	}
	// Bytes per frame: samples/8 bytes of bitrate per sample period.
	size = (samples/8)*kbps*1000/rate + padding
	if size < 4 {
		return 0, 0, 0, errNotMPEGFrame
	}
	return size, samples, rate, nil
}
