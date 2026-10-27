package modules

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func touch(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A source folder becomes one group per album: a disc folder folds into the album
// above it, hidden folders and non-audio files are not offered, and loose files at the
// root are a group of their own.
func TestScanImportSourceGroupsByAlbum(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "Band", "Album (2001)", "01 One.flac"), "x")
	touch(t, filepath.Join(root, "Band", "Album (2001)", "cover.jpg"), "x")
	touch(t, filepath.Join(root, "Band", "Double (1999)", "CD 01", "01 A.flac"), "x")
	touch(t, filepath.Join(root, "Band", "Double (1999)", "CD 02", "01 B.flac"), "x")
	touch(t, filepath.Join(root, ".stfolder", "01 Hidden.flac"), "x")
	touch(t, filepath.Join(root, "loose.mp3"), "x")

	groups, err := ScanImportSource(root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		root: 1,
		filepath.Join(root, "Band", "Album (2001)"):  1,
		filepath.Join(root, "Band", "Double (1999)"): 2,
	}
	if len(groups) != len(want) {
		t.Fatalf("got %d groups, want %d: %+v", len(groups), len(want), groups)
	}
	for _, g := range groups {
		if want[g.Folder] != len(g.Files) {
			t.Errorf("group %s has %d files, want %d", g.Folder, len(g.Files), want[g.Folder])
		}
		if g.Folder == filepath.Join(root, "Band", "Double (1999)") && (g.Album != "Double" || g.Year != 1999 || g.Artist != "Band") {
			t.Errorf("hint for the double album = %q/%q/%d", g.Artist, g.Album, g.Year)
		}
	}
}

func TestScanImportSourceSingleFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "01 One.flac")
	touch(t, file, "x")
	groups, err := ScanImportSource(file)
	if err != nil || len(groups) != 1 || groups[0].Files[0] != file {
		t.Fatalf("groups = %+v, %v", groups, err)
	}
	if _, err := ScanImportSource(filepath.Join(t.TempDir(), "notes.txt")); err == nil {
		t.Error("a missing path should be an error")
	}
}

func TestTransferFile(t *testing.T) {
	dir := t.TempDir()
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			return "<missing>"
		}
		return string(b)
	}

	// Copy leaves the source and creates the folders.
	src := filepath.Join(dir, "in", "a.flac")
	touch(t, src, "new")
	target := filepath.Join(dir, "lib", "Artist", "Album", "01 a.flac")
	if err := TransferFile(src, target, TransferCopy); err != nil {
		t.Fatal(err)
	}
	if read(src) != "new" || read(target) != "new" {
		t.Errorf("copy: source %q, target %q", read(src), read(target))
	}

	// Move replaces what is at the target and takes the source away.
	touch(t, target, "old")
	if err := TransferFile(src, target, TransferMove); err != nil {
		t.Fatal(err)
	}
	if read(src) != "<missing>" || read(target) != "new" {
		t.Errorf("move: source %q, target %q", read(src), read(target))
	}

	// No temporary file is left beside the target.
	entries, _ := os.ReadDir(filepath.Dir(target))
	if len(entries) != 1 {
		t.Errorf("target folder holds %d entries, want only the file", len(entries))
	}

	if err := TransferFile(filepath.Join(dir, "gone.flac"), target, TransferMove); err == nil {
		t.Error("a missing source must fail")
	}
	if read(target) != "new" {
		t.Error("a failed transfer must leave the target alone")
	}
}

func TestRemoveEmptyDirs(t *testing.T) {
	root := t.TempDir()
	emptied := filepath.Join(root, "Band", "Album", "CD 01")
	kept := filepath.Join(root, "Band", "Other")
	touch(t, filepath.Join(kept, "cover.jpg"), "x")
	if err := os.MkdirAll(emptied, 0o755); err != nil {
		t.Fatal(err)
	}

	RemoveEmptyDirs(emptied, root)
	if _, err := os.Stat(filepath.Join(root, "Band", "Album")); !os.IsNotExist(err) {
		t.Error("the emptied album folder should be gone")
	}
	if _, err := os.Stat(kept); err != nil {
		t.Error("a folder still holding a file must stay")
	}
	if _, err := os.Stat(root); err != nil {
		t.Error("the import root is never removed")
	}
}

func TestPathWithin(t *testing.T) {
	sep := string(filepath.Separator)
	cases := []struct {
		path, root string
		want       bool
	}{
		{sep + "music", sep + "music", true},
		{filepath.Join(sep+"music", "a"), sep + "music", true},
		{sep + "music2", sep + "music", false},
		{sep + "mus", sep + "music", false},
		{filepath.Join(sep+"music", "..", "x"), sep + "music", false},
		{filepath.Join(sep+"music", "..a"), sep + "music", true},
	}
	for _, tc := range cases {
		if got := PathWithin(tc.path, tc.root); got != tc.want {
			t.Errorf("PathWithin(%q, %q) = %v, want %v", tc.path, tc.root, got, tc.want)
		}
	}
}

// A group's embedded release ID is offered only when every tagged file agrees on it.
func TestScanImportSourceReadsAgreedReleaseID(t *testing.T) {
	requireTool(t, "metaflac")
	root := t.TempDir()
	tag := func(dir, name, id string) {
		src := synthAudio(t, ".flac")
		dst := filepath.Join(root, dir, name)
		if err := TransferFile(src, dst, TransferMove); err != nil {
			t.Fatal(err)
		}
		if id != "" {
			if out, err := exec.Command("metaflac", "--set-tag=MUSICBRAINZ_ALBUMID="+id, dst).CombinedOutput(); err != nil {
				t.Fatalf("metaflac: %v %s", err, out)
			}
		}
	}
	tag("Agreed", "01 a.flac", "rel-1")
	tag("Agreed", "02 b.flac", "rel-1")
	tag("Agreed", "03 c.flac", "") // an untagged file does not veto
	tag("Mixed", "01 a.flac", "rel-1")
	tag("Mixed", "02 b.flac", "rel-2")

	groups, err := ScanImportSource(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		switch filepath.Base(g.Folder) {
		case "Agreed":
			if g.ReleaseID != "rel-1" || g.ReleaseIDsDisagree {
				t.Errorf("agreed group: %q, disagree=%v", g.ReleaseID, g.ReleaseIDsDisagree)
			}
		case "Mixed":
			if g.ReleaseID != "" || !g.ReleaseIDsDisagree {
				t.Errorf("mixed group: %q, disagree=%v", g.ReleaseID, g.ReleaseIDsDisagree)
			}
		}
	}
}

func TestSuggestFolders(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"Downloads", "downloads-old", "Docs", ".hidden", "Music"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	touch(t, filepath.Join(root, "Doc file.txt"), "x")
	sep := string(filepath.Separator)

	got := SuggestFolders(filepath.Join(root, "do"), 10)
	want := []string{filepath.Join(root, "Docs"), filepath.Join(root, "Downloads"), filepath.Join(root, "downloads-old")}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("prefix: got %v, want %v (case-insensitive, folders only)", got, want)
	}
	if got := SuggestFolders(root+sep, 10); len(got) != 4 {
		t.Errorf("children: got %v, want the four visible folders", got)
	}
	if got := SuggestFolders(filepath.Join(root, ".h"), 10); len(got) != 1 {
		t.Errorf("a dot prefix asks for hidden folders: got %v", got)
	}
	if got := SuggestFolders(root+sep, 2); len(got) != 2 {
		t.Errorf("limit: got %d", len(got))
	}
	for _, input := range []string{"", "relative/path", filepath.Join(root, "missing", "x")} {
		if got := SuggestFolders(input, 10); got != nil {
			t.Errorf("SuggestFolders(%q) = %v, want nothing", input, got)
		}
	}
}

func TestParseAlbumFolderName(t *testing.T) {
	cases := []struct {
		name, artist, album string
		year                int
	}{
		{"Fleetwood Mac - Rumours (1977) [FLAC]", "Fleetwood Mac", "Rumours", 1977},
		{"Slint - 1991 - Spiderland", "Slint", "Spiderland", 1991},
		{"Slint - Spiderland - 1991", "Slint", "Spiderland", 1991},
		{"Rumours (1977)", "", "Rumours", 1977},
		{"Slint.Spiderland.1991.WEB", "", "Slint Spiderland", 1991},
		{"Slint-Spiderland-WEB-2014-GRP", "Slint", "Spiderland", 2014},
		{"Godspeed You! Black Emperor - Lift Your Skinny Fists Like Antennas to Heaven", "Godspeed You! Black Emperor", "Lift Your Skinny Fists Like Antennas to Heaven", 0},
		{"Artist - Album - Part Two {Deluxe}", "Artist", "Album - Part Two", 0},
		{"untitled rip", "", "untitled rip", 0},
	}
	for _, tc := range cases {
		artist, album, year := ParseAlbumFolderName(tc.name)
		if artist != tc.artist || album != tc.album || year != tc.year {
			t.Errorf("ParseAlbumFolderName(%q) = %q, %q, %d; want %q, %q, %d", tc.name, artist, album, year, tc.artist, tc.album, tc.year)
		}
	}
}

// The parent folder names the artist only inside the scanned root: above it, it is
// wherever the downloads live ("config", "downloads"), which is no artist at all.
func TestImportHintNeverReadsTheArtistFromAboveTheRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	touch(t, filepath.Join(root, "Rumours (1977)", "01 Dreams.flac"), "x")
	touch(t, filepath.Join(root, "Fleetwood Mac", "Tusk (1979)", "01 Over and Over.flac"), "x")

	groups, err := ScanImportSource(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		switch filepath.Base(g.Folder) {
		case "Rumours (1977)":
			if g.Artist != "" || g.Album != "Rumours" || g.AlbumFrom != HintFromFolder || g.Year != 1977 {
				t.Errorf("album directly under the root: %+v", g)
			}
		case "Tusk (1979)":
			if g.Artist != "Fleetwood Mac" || g.ArtistFrom != HintFromFolder {
				t.Errorf("an artist folder inside the root names the artist: %+v", g)
			}
		}
	}
}

// Tags come before any folder name, and the value most files agree on wins.
func TestImportHintPrefersTags(t *testing.T) {
	requireTool(t, "metaflac")
	root := t.TempDir()
	write := func(name string, tags ...string) {
		dst := filepath.Join(root, "config", "whatever folder", name)
		if err := TransferFile(synthAudio(t, ".flac"), dst, TransferMove); err != nil {
			t.Fatal(err)
		}
		args := []string{}
		for _, tag := range tags {
			args = append(args, "--set-tag="+tag)
		}
		if out, err := exec.Command("metaflac", append(args, dst)...).CombinedOutput(); err != nil {
			t.Fatalf("metaflac: %v %s", err, out)
		}
	}
	write("01.flac", "ALBUMARTIST=Slint", "ARTIST=Slint", "ALBUM=Spiderland", "DATE=1991-03-27")
	write("02.flac", "ALBUMARTIST=Slint", "ARTIST=Guest", "ALBUM=Spiderland", "DATE=1991")
	write("03.flac", "ARTIST=Stray", "ALBUM=Wrong")

	groups, err := ScanImportSource(filepath.Join(root, "config"))
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups = %+v, %v", groups, err)
	}
	g := groups[0]
	if g.Artist != "Slint" || g.ArtistFrom != HintFromTags || g.Album != "Spiderland" || g.AlbumFrom != HintFromTags || g.Year != 1991 || g.YearFrom != HintFromTags {
		t.Errorf("hint = %+v", g)
	}
	if g.Discs != 1 {
		t.Errorf("discs = %d", g.Discs)
	}
}

// The tags that tell editions apart are read alongside the hint, each the value most
// files agree on, and the release's own date rather than the original's.
func TestImportHintReadsEditionClues(t *testing.T) {
	requireTool(t, "metaflac")
	root := t.TempDir()
	write := func(name string, tags ...string) {
		dst := filepath.Join(root, "Album", name)
		if err := TransferFile(synthAudio(t, ".flac"), dst, TransferMove); err != nil {
			t.Fatal(err)
		}
		args := []string{}
		for _, tag := range tags {
			args = append(args, "--set-tag="+tag)
		}
		if out, err := exec.Command("metaflac", append(args, dst)...).CombinedOutput(); err != nil {
			t.Fatalf("metaflac: %v %s", err, out)
		}
	}
	edition := []string{"BARCODE=00602478023293", "CATALOGNUMBER=4780232", "LABEL=Polydor", "RELEASECOUNTRY=GB", "MEDIA=CD", "ORIGINALDATE=1985-09-16", "DATE=2018-11-16"}
	write("01.flac", edition...)
	write("02.flac", edition...)
	write("03.flac", "BARCODE=111")

	groups, err := ScanImportSource(root)
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups = %+v, %v", groups, err)
	}
	want := ImportEditionClues{Barcode: "00602478023293", CatalogNumber: "4780232", Label: "Polydor", Country: "GB", Media: "CD", Date: "2018-11-16"}
	if got := groups[0].Edition; got == nil || *got != want {
		t.Errorf("edition = %+v, want %+v", got, want)
	}
}

// Untagged files say nothing about their edition, and the group says so with nil.
func TestImportHintWithoutEditionClues(t *testing.T) {
	if _, _, _, edition := tagHint(nil); edition != nil {
		t.Errorf("edition = %+v, want nil", edition)
	}
}
