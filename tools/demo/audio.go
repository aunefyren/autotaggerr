package main

import (
	"bytes"
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
func FLACBytes(tags FileTags, lengthMS int, audioSeed uint64) ([]byte, error) {
	info := &meta.StreamInfo{
		BlockSizeMin: blockSize, BlockSizeMax: blockSize,
		SampleRate: sampleRate, NChannels: 1, BitsPerSample: 16,
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
	for i := range samples {
		samples[i] = int32(r.Intn(65536) - 32768)
	}
	f := &frame.Frame{
		Header:    frame.Header{HasFixedBlockSize: true, BlockSize: blockSize, SampleRate: sampleRate, Channels: frame.ChannelsMono, BitsPerSample: 16},
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

// mp3FrameHeader is MPEG-1 Layer III, 128 kbps, 44.1 kHz, joint stereo, no CRC: a
// 417-byte frame.
var mp3FrameHeader = []byte{0xFF, 0xFB, 0x90, 0x64}

const mp3FrameSize = 417

// MP3Bytes builds an MP3 file in memory.
func MP3Bytes(tags FileTags, audioSeed uint64) ([]byte, error) {
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
	r := rand.New(rand.NewSource(int64(audioSeed)))
	for f := 0; f < 4; f++ {
		frameBytes := make([]byte, mp3FrameSize)
		copy(frameBytes, mp3FrameHeader)
		for i := len(mp3FrameHeader); i < mp3FrameSize; i++ {
			frameBytes[i] = byte(r.Intn(256))
		}
		buf.Write(frameBytes)
	}
	return buf.Bytes(), nil
}

// writeAudio writes a file of the given format.
func writeAudio(path, format string, tags FileTags, lengthMS int, audioSeed uint64) error {
	var data []byte
	var err error
	if format == "mp3" {
		data, err = MP3Bytes(tags, audioSeed)
	} else {
		data, err = FLACBytes(tags, lengthMS, audioSeed)
	}
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
