package main

import (
	"bytes"
	"encoding/binary"
	"hash/fnv"
	"math/rand"
	"os"

	"github.com/bogem/id3v2"
	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/frame"
	"github.com/mewkiz/flac/meta"
)

// Tiny audio files: valid enough for everything the app reads, a few KB each.
//
// A FLAC is a real stream: STREAMINFO claiming the track's full length (what the
// duration read and the disc guard see), the MusicBrainz IDs as Vorbis comments, room
// for metaflac to write into, and one short frame of samples drawn from the audio
// seed. The frame is what modules.AudioPayloadHash hashes, so two files with one seed
// are the same audio whatever their tags, and two seeds never are.
//
// An MP3 is an ID3v2 tag followed by a few MPEG-1 Layer III frames whose payload is
// drawn from the seed the same way.

// Encoding is how a generated file is encoded, which is what its quality is measured
// from: FLAC's bit depth, an MP3's bitrate or VBR preset. It is chosen per album
// (encodingFor), never per file or per holding, so every copy of an album — the
// identical-audio duplicate scenarios included — is encoded alike.
type Encoding struct {
	BitDepth int  // FLAC: 16 or 24
	Kbps     int  // CBR MP3: 128, 192, 256 or 320
	VBRV0    bool // MP3: LAME V0, stated in a Xing header
}

// encodingFor picks an album's encoding from a hash of its ID, so the universe's own
// random stream is untouched and every earlier scenario generates as it did. Most
// FLAC is 16-bit, a quarter 24-bit; MP3 runs from 320 down to a few 128s, so a
// Lossless profile has plenty to ask upgrades for and a lossy one a few.
func encodingFor(releaseGroupID, format string) Encoding {
	h := fnv.New32a()
	h.Write([]byte(releaseGroupID))
	n := h.Sum32() % 100
	if format != "mp3" {
		if n < 25 {
			return Encoding{BitDepth: 24}
		}
		return Encoding{BitDepth: 16}
	}
	switch {
	case n < 40:
		return Encoding{Kbps: 320}
	case n < 65:
		return Encoding{VBRV0: true}
	case n < 80:
		return Encoding{Kbps: 256}
	case n < 92:
		return Encoding{Kbps: 192}
	}
	return Encoding{Kbps: 128}
}

// FileTags is what a generated file is tagged with.
type FileTags struct {
	Title, Artist, Album, AlbumArtist string
	Track, Disc                       int
	Date                              string
	ReleaseID, ReleaseGroupID         string
	ReleaseTrackID, RecordingID       string
	AlbumArtistID                     string
}

const (
	sampleRate = 44100
	blockSize  = 256
)

func (t FileTags) vorbis() [][2]string {
	tags := [][2]string{
		{"TITLE", t.Title}, {"ARTIST", t.Artist}, {"ALBUM", t.Album}, {"ALBUMARTIST", t.AlbumArtist},
		{"TRACKNUMBER", itoa(t.Track)}, {"DISCNUMBER", itoa(t.Disc)}, {"DATE", t.Date},
	}
	for _, kv := range [][2]string{
		{"MUSICBRAINZ_ALBUMID", t.ReleaseID}, {"MUSICBRAINZ_RELEASEGROUPID", t.ReleaseGroupID},
		{"MUSICBRAINZ_RELEASETRACKID", t.ReleaseTrackID}, {"MUSICBRAINZ_TRACKID", t.RecordingID},
		{"MUSICBRAINZ_ALBUMARTISTID", t.AlbumArtistID},
	} {
		if kv[1] != "" {
			tags = append(tags, kv)
		}
	}
	return tags
}

// FLACBytes builds a FLAC file in memory.
func FLACBytes(tags FileTags, lengthMS int, audioSeed uint64, bitDepth int) ([]byte, error) {
	if bitDepth != 24 {
		bitDepth = 16
	}
	info := &meta.StreamInfo{
		BlockSizeMin: blockSize, BlockSizeMax: blockSize,
		SampleRate: sampleRate, NChannels: 1, BitsPerSample: uint8(bitDepth),
		NSamples: uint64(lengthMS) * sampleRate / 1000,
	}
	// Length only has to be non-zero: the encoder treats 0 as an empty block and
	// computes the real length from the body otherwise.
	comment := &meta.Block{Header: meta.Header{Type: meta.TypeVorbisComment, Length: 1}, Body: &meta.VorbisComment{Vendor: "autotaggerr demo", Tags: tags.vorbis()}}
	padding := &meta.Block{Header: meta.Header{Type: meta.TypePadding, Length: 1024}}

	var buf bytes.Buffer
	enc, err := flac.NewEncoder(&buf, info, comment, padding)
	if err != nil {
		return nil, err
	}
	enc.EnablePredictionAnalysis(false)
	r := rand.New(rand.NewSource(int64(audioSeed)))
	samples := make([]int32, blockSize)
	span := 1 << bitDepth
	for i := range samples {
		samples[i] = int32(r.Intn(span) - span/2)
	}
	f := &frame.Frame{
		Header:    frame.Header{HasFixedBlockSize: true, BlockSize: blockSize, SampleRate: sampleRate, Channels: frame.ChannelsMono, BitsPerSample: uint8(bitDepth)},
		Subframes: []*frame.Subframe{{SubHeader: frame.SubHeader{Pred: frame.PredVerbatim}, Samples: samples, NSamples: blockSize}},
	}
	if err := enc.WriteFrame(f); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// mp3Frame is an MPEG-1 Layer III frame header at 44.1 kHz, joint stereo, no CRC, and
// its size, for a bitrate the demo uses.
func mp3Frame(kbps int) ([]byte, int) {
	index := map[int]byte{128: 9, 192: 11, 224: 12, 256: 13, 320: 14}[kbps]
	if index == 0 {
		kbps, index = 128, 9
	}
	return []byte{0xFF, 0xFB, index << 4, 0x64}, 144000 * kbps / sampleRate
}

// mp3VBRRates are the bitrates of a V0 file's audio frames: varying, as VBR does.
var mp3VBRRates = []int{256, 320, 224, 256}

// xingFrame is the info frame LAME puts first in a VBR file: a 128 kbit/s frame whose
// side info is followed by a Xing header with the frame and byte totals, an empty
// TOC, and the quality field — 100 - 10·V - q, here V0 at q = 2.
func xingFrame(frames, bytes int) []byte {
	header, size := mp3Frame(128)
	out := make([]byte, size)
	copy(out, header)
	x := 4 + 32
	copy(out[x:], "Xing")
	binary.BigEndian.PutUint32(out[x+4:], 0xF)
	binary.BigEndian.PutUint32(out[x+8:], uint32(frames))
	binary.BigEndian.PutUint32(out[x+12:], uint32(bytes))
	binary.BigEndian.PutUint32(out[x+16+100:], 98)
	return out
}

// MP3Bytes builds an MP3 file in memory: CBR at enc.Kbps, or V0 with a Xing frame.
func MP3Bytes(tags FileTags, audioSeed uint64, enc Encoding) ([]byte, error) {
	tag := id3v2.NewEmptyTag()
	tag.SetVersion(4)
	tag.SetDefaultEncoding(id3v2.EncodingUTF8)
	tag.SetTitle(tags.Title)
	tag.SetArtist(tags.Artist)
	tag.SetAlbum(tags.Album)
	tag.SetYear(tags.Date)
	tag.AddTextFrame("TPE2", id3v2.EncodingUTF8, tags.AlbumArtist)
	tag.AddTextFrame("TRCK", id3v2.EncodingUTF8, itoa(tags.Track))
	tag.AddTextFrame("TPOS", id3v2.EncodingUTF8, itoa(tags.Disc))
	for _, kv := range [][2]string{
		{"MusicBrainz Album Id", tags.ReleaseID}, {"MusicBrainz Release Group Id", tags.ReleaseGroupID},
		{"MusicBrainz Release Track Id", tags.ReleaseTrackID}, {"MusicBrainz Recording Id", tags.RecordingID},
		{"MusicBrainz Album Artist Id", tags.AlbumArtistID},
	} {
		if kv[1] != "" {
			tag.AddUserDefinedTextFrame(id3v2.UserDefinedTextFrame{Encoding: id3v2.EncodingUTF8, Description: kv[0], Value: kv[1]})
		}
	}
	var buf bytes.Buffer
	if _, err := tag.WriteTo(&buf); err != nil {
		return nil, err
	}
	rates := []int{enc.Kbps, enc.Kbps, enc.Kbps, enc.Kbps}
	if enc.VBRV0 {
		rates = mp3VBRRates
		total := 0
		for _, kbps := range rates {
			_, size := mp3Frame(kbps)
			total += size
		}
		buf.Write(xingFrame(len(rates), total))
	}
	r := rand.New(rand.NewSource(int64(audioSeed)))
	for _, kbps := range rates {
		header, size := mp3Frame(kbps)
		frameBytes := make([]byte, size)
		copy(frameBytes, header)
		for i := len(header); i < size; i++ {
			frameBytes[i] = byte(r.Intn(256))
		}
		buf.Write(frameBytes)
	}
	return buf.Bytes(), nil
}

// writeAudio writes a file of the given format.
func writeAudio(path, format string, tags FileTags, lengthMS int, audioSeed uint64, enc Encoding) error {
	var data []byte
	var err error
	if format == "mp3" {
		data, err = MP3Bytes(tags, audioSeed, enc)
	} else {
		data, err = FLACBytes(tags, lengthMS, audioSeed, enc.BitDepth)
	}
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
