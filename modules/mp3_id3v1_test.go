package modules

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// hasID3v1 reports whether a file ends in the 128-byte ID3v1 trailer.
func hasID3v1(t *testing.T, path string) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(data) < id3v1TagSize {
		return false
	}
	return string(data[len(data)-id3v1TagSize:][:3]) == "TAG"
}

// writeTail builds a file whose last bytes are the given trailer(s), with enough
// filler in front to stand in for audio.
func writeTail(t *testing.T, trailers ...[]byte) string {
	t.Helper()
	body := bytes.Repeat([]byte{0x55}, 512)
	for _, trailer := range trailers {
		body = append(body, trailer...)
	}
	path := filepath.Join(t.TempDir(), "tail.mp3")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func id3v1Block() []byte {
	block := make([]byte, id3v1TagSize)
	copy(block, "TAG")
	copy(block[3:], "a title that ID3v1 truncates")
	return block
}

func id3v1ExtendedBlock() []byte {
	block := make([]byte, id3v1ExtendedTagSize)
	copy(block, "TAG+")
	return block
}

// stripID3v1 is a truncation, so the thing to pin is that it truncates exactly the
// trailer and never a byte of audio.
func TestStripID3v1(t *testing.T) {
	filler := int64(512)

	cases := []struct {
		name     string
		path     string
		want     bool
		wantSize int64
	}{
		{
			name:     "no trailer leaves the file alone",
			path:     writeTail(t),
			want:     false,
			wantSize: filler,
		},
		{
			name:     "the trailer goes",
			path:     writeTail(t, id3v1Block()),
			want:     true,
			wantSize: filler,
		},
		{
			// Removing the base tag and leaving the enhanced block would orphan a
			// structure whose header is gone.
			name:     "the enhanced block goes with it",
			path:     writeTail(t, id3v1ExtendedBlock(), id3v1Block()),
			want:     true,
			wantSize: filler,
		},
		{
			// "TAG+" only means anything directly in front of a real ID3v1 tag. On
			// its own those bytes are audio that happens to read that way.
			name:     "an enhanced block without a trailer is not touched",
			path:     writeTail(t, id3v1ExtendedBlock()),
			want:     false,
			wantSize: filler + id3v1ExtendedTagSize,
		},
		{
			name:     "a file shorter than a trailer cannot have one",
			path:     writeShortFile(t),
			want:     false,
			wantSize: 10,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			removed, err := stripID3v1(tc.path)
			if err != nil {
				t.Fatalf("stripID3v1: %v", err)
			}
			if removed != tc.want {
				t.Errorf("removed = %v, want %v", removed, tc.want)
			}
			info, err := os.Stat(tc.path)
			if err != nil {
				t.Fatalf("stat: %v", err)
			}
			if info.Size() != tc.wantSize {
				t.Errorf("size = %d, want %d", info.Size(), tc.wantSize)
			}
		})
	}
}

func writeShortFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "short.mp3")
	if err := os.WriteFile(path, bytes.Repeat([]byte{0x55}, 10), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// Every MP3 Autotaggerr tagged through the ffmpeg writer carries an ID3v1 trailer,
// because that writer was passed -write_id3v1 1 and rebuilt it on every pass. Nothing
// rebuilds it now, so the first write by the current engine has to take it off: a file
// whose v2 tag says one thing and whose v1 tag says another gives a v1-only reader no
// way to tell which half is current.
func TestSetMP3TagsStripsTheLegacyID3v1Trailer(t *testing.T) {
	path := synthAudio(t, ".mp3")
	meta := fullFileTags()
	writeLegacyMP3Tags(t, path, meta)

	if !hasID3v1(t, path) {
		t.Fatal("the legacy fixture has no ID3v1 trailer, so this test proves nothing")
	}

	// Any real change is enough; the strip rides along with a write, it does not
	// cause one.
	meta.Title = "A Different Title"
	unchanged, written, _, err := SetMP3Tags(path, meta, models.TaggerSettings{})
	if err != nil {
		t.Fatalf("SetMP3Tags: %v", err)
	}
	if unchanged || written == 0 {
		t.Fatalf("expected a write, got unchanged=%v written=%d", unchanged, written)
	}

	if hasID3v1(t, path) {
		t.Error("the ID3v1 trailer survived a tag write")
	}

	// The v2 tag is what everything here reads, so the truncation must not have
	// reached anything that matters.
	tags, err := GetMP3Tags(path)
	if err != nil {
		t.Fatalf("GetMP3Tags: %v", err)
	}
	if got := tags["TITLE"]; len(got) != 1 || got[0] != "A Different Title" {
		t.Errorf("TITLE = %q after stripping, want the value just written", got)
	}
}

// The skip-unchanged path must stay a genuine skip. A trailing v1 tag is not a reason
// to rewrite a file whose v2 tag is already correct — that would turn the first run
// after this change into a full-library rewrite.
//
// The fixture is settled first. A legacy file now genuinely needs one migrating write
// (UFID, and the ISRC moving to TSRC — see TestLegacyFfmpegFilesConvergeAfterOneRewrite),
// and that write also strips the trailer, so the trailer is put back afterwards. What
// is being tested is the *second* write, where the v2 tag is correct and the trailer is
// the only thing left that could wrongly trigger one.
func TestID3v1DoesNotForceARewrite(t *testing.T) {
	path := synthAudio(t, ".mp3")
	meta := fullFileTags()
	writeLegacyMP3Tags(t, path, meta)

	if _, _, _, err := SetMP3Tags(path, meta, models.TaggerSettings{}); err != nil {
		t.Fatalf("settling write: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(path, append(data, id3v1Block()...), 0o644); err != nil {
		t.Fatalf("re-add trailer: %v", err)
	}

	unchanged, written, _, err := SetMP3Tags(path, meta, models.TaggerSettings{})
	if err != nil {
		t.Fatalf("SetMP3Tags: %v", err)
	}
	if !unchanged || written != 0 {
		t.Fatalf("a correctly tagged file was rewritten: unchanged=%v written=%d", unchanged, written)
	}
	if !hasID3v1(t, path) {
		t.Error("the trailer was removed without a write, so the file was touched for nothing")
	}
}
