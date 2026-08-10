package modules

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/utilities"
	"github.com/bogem/id3v2"
)

// id3MultiValueSeparator is the null byte ID3v2.4 puts between the several values of
// one text frame. It is not a delimiter anyone picked — it is what the format says.
const id3MultiValueSeparator = "\x00"

// renderMP3Values turns a field's values into what the file will say they are.
//
// Unlike FLAC there is no free win available here, which is why this is a setting and
// not a fix. The spec-correct form is a null-separated frame, and ffmpeg reads only
// the *first* value out of one — so a spec-correct MP3 shows a single genre to
// everything ffmpeg-backed, Plex included, where the joined value shows several. The
// two forms serve different readers and nothing serves both; see
// models.TaggerSettings.MP3MultiValueTags.
//
// Whichever form is chosen, what this returns is what GetMP3Tags will read back — the
// writer encodes and the reader decodes the null separator symmetrically, so flipping
// the setting re-tags a file exactly once and then converges.
func renderMP3Values(values []string, multiValue bool) []string {
	if multiValue {
		return utilities.NormalizeTagValues(values)
	}
	joined := utilities.JoinTagValues(values)
	if joined == "" {
		return nil
	}
	return []string{joined}
}

// renderMP3Tags applies renderMP3Values across a whole desired-tag map.
func renderMP3Tags(desired map[string][]string, tagger models.TaggerSettings) map[string][]string {
	rendered := make(map[string][]string, len(desired))
	for key, values := range desired {
		rendered[key] = renderMP3Values(values, tagger.MP3MultiValueTags)
	}
	return rendered
}

// encodeID3FrameValue renders a field's values as one frame payload. With one value
// it is that value, so the single-valued form needs no special case.
func encodeID3FrameValue(values []string) string {
	return strings.Join(values, id3MultiValueSeparator)
}

// buildMP3DesiredTags maps resolved metadata onto the ID3 keys we write. Kept
// pure (no I/O) so the field-to-tag wiring can be unit-tested. Values are carried as
// the several values they are; renderMP3Values decides how they reach the file.
func buildMP3DesiredTags(metadata models.FileTags) map[string][]string {
	return map[string][]string{
		"ARTIST":      single(metadata.Artist),
		"ARTISTS":     metadata.Artists,
		"ALBUMARTIST": single(metadata.AlbumArtist),
		// Single-valued for Plex, which renders a joined string as one artist named
		// "A; B"; ALBUMARTISTS carries the full credit alongside it.
		"ALBUMARTISTS": metadata.AlbumArtists,
		"GENRE":        metadata.Genres,
		"ALBUM":        single(metadata.Album),
		"TITLE":        single(metadata.Title),
		"TRACKNUMBER":  single(metadata.Track),
		"DISCNUMBER":   single(metadata.DiscNumber),

		// Totals ride along in the composite TRCK/TPOS frames ("3/12", "1/2")
		// written below — the standard ID3 representation, which GetMP3Tags splits
		// back apart.
		"TRACKTOTAL": single(metadata.TrackTotal),
		"DISCTOTAL":  single(metadata.DiscTotal),
		// ISRC goes to the standard TSRC frame. Files written before that carry it in
		// the legacy TXXX artefact instead; GetMP3Tags reads both.
		"ISRC": metadata.ISRCs,

		"SCRIPT":        single(metadata.Script),
		"TMED":          single(metadata.Media),
		"publisher":     metadata.RecordLabels,
		"BARCODE":       single(metadata.Barcode),
		"CATALOGNUMBER": metadata.CatalogNumbers,

		// Release
		"DATE": single(metadata.ReleaseDate), // maps to TDRC
		// Lower case deliberately: ID3v2.4 dropped TYER, so the year lands in a TXXX
		// frame described by this key, and ffmpeg spelled it "year" when it wrote it.
		// The reader is case-insensitive, so a mismatch here would not show up as a
		// diff — it would just quietly leave a second, differently-cased frame on
		// every file. TestNewWriterReproducesTheLegacyFrames is what catches that.
		"year": single(metadata.ReleaseYear),

		// Original release
		"TDOR":         single(metadata.OriginalDate), // maps to TDOR
		"originaldate": single(metadata.OriginalDate),
		"originalyear": single(metadata.OriginalYear),

		// MUSICBRAINZ
		"MusicBrainz Album Status":          single(metadata.MBAlbumStatus),
		"MusicBrainz Album Type":            single(metadata.MBAlbumType),
		"MusicBrainz Album Release Country": single(metadata.MBAlbumReleaseCountry),
		"MusicBrainz Album Id":              single(metadata.MBAlbumID),
		"MusicBrainz Artist Id":             metadata.MBArtistIDs,
		"MusicBrainz Album Artist Id":       metadata.MBAlbumArtistIDs,
		"MusicBrainz Release Group Id":      single(metadata.MBReleaseGroupID),
		"MusicBrainz Release Track Id":      single(metadata.MBReleaseTrackID),
		// The recording MBID is the identifier that survives a release being merged
		// or superseded, so an MP3 without it is harder to re-identify from its own
		// tags than the same track as FLAC (which has carried MUSICBRAINZ_TRACKID all
		// along). The TXXX spelling is the one extractFromID3v2 already reads back
		// for the "recording" metadata type, so writing it here closes that loop.
		"MusicBrainz Recording Id": single(metadata.MBRecordingID),
		// The same MBID again, in the UFID frame — Picard's canonical home for it,
		// and the only one a reader can identify without agreeing on a TXXX
		// description first, since the owner string is part of the frame. Written
		// under its own key for the reason ufidTagKey explains.
		ufidTagKey: single(metadata.MBRecordingID),
	}
}

// pairedFrameValue renders the "n/total" halves of a paired ID3 frame (track, disc).
// An empty total drops the suffix, and an empty number yields "" — which is how the
// frame is cleared, since the pair shares one frame and cannot be half-removed.
func pairedFrameValue(number, total string) string {
	if number == "" {
		return ""
	}
	if total == "" {
		return number
	}
	return fmt.Sprintf("%s/%s", number, total)
}

// splitPairedFrameValue is the read direction of pairedFrameValue.
func splitPairedFrameValue(value string) (number, total string) {
	parts := strings.SplitN(value, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return value, ""
}

// id3TextFrameForKey maps a desired-tag key — upper-cased, as the diff returns it —
// onto the standard ID3 frame that holds it. A key that is not here is written as a
// TXXX frame described by the key itself.
//
// That split is not a design choice so much as a description of what is already on
// disk: ffmpeg turned any metadata key it did not recognise into a TXXX frame with
// the key as its description, so every MP3 Autotaggerr has ever tagged looks like
// this. Reproducing it exactly is what lets the engine change without re-tagging a
// single file.
var id3TextFrameForKey = map[string]string{
	"ARTIST":      "TPE1",
	"ALBUMARTIST": "TPE2",
	"ALBUM":       "TALB",
	"TITLE":       "TIT2",
	"GENRE":       "TCON",
	"DATE":        "TDRC",
	"TDOR":        "TDOR",
	"TMED":        "TMED",
	"PUBLISHER":   "TPUB",
	// TSRC is the standard frame for the ISRC. It replaced the TXXX artefact
	// described by legacyISRCFrameDescription; being in this map gives it the read
	// direction for free, through id3KeyForTextFrame.
	"ISRC": "TSRC",
}

// id3PairedFrames are the two frames that carry a number and a total in one value.
// Both halves are written together because the frame cannot be half-removed.
var id3PairedFrames = map[string][2]string{
	"TRCK": {"TRACKNUMBER", "TRACKTOTAL"},
	"TPOS": {"DISCNUMBER", "DISCTOTAL"},
}

// id3KeyForTextFrame is the read direction of id3TextFrameForKey, derived from it so
// the two cannot drift apart.
var id3KeyForTextFrame = func() map[string]string {
	reverse := make(map[string]string, len(id3TextFrameForKey))
	for key, frameID := range id3TextFrameForKey {
		reverse[frameID] = key
	}
	return reverse
}()

// legacyISRCFrameDescription is the description of the TXXX frame the ISRC *used* to
// live in, kept only so files written before the move still read.
//
// It is not "ISRC" but the literal string "TXXX", which was an artefact: the original
// writer passed `-metadata TXXX=ISRC:<value>` to ffmpeg, which stored a user-defined
// frame *described* "TXXX" whose value carries its own "KEY:value" packing — the only
// way that writer could reach a user-defined frame at all. The ISRC now goes to TSRC,
// the standard frame for it (see id3TextFrameForKey), and any file still carrying the
// legacy frame is migrated on its next write (see migrateLegacyISRCFrame).
//
// Read support is not transitional and should not be removed: a library tagged by an
// older Autotaggerr keeps these frames until something rewrites each file, and a file
// nothing ever changes is never rewritten at all.
const legacyISRCFrameDescription = "TXXX"

// ufidTagKey is the desired-tag key for the UFID frame's payload.
//
// It is deliberately *not* "MusicBrainz Recording Id", even though it carries the same
// recording MBID: that key already names the TXXX frame, and one key naming two frames
// would read the MBID twice into the same slot, report drift that no write can settle,
// and rewrite the file on every scan forever. Two frames, two keys, one diff each.
const ufidTagKey = "UFID"

// musicBrainzUFIDOwner is the owner identifier written into the UFID frame. Picard's
// canonical value, so a file tagged here and a file tagged by Picard agree.
const musicBrainzUFIDOwner = "http://musicbrainz.org"

// musicBrainzUFIDOwnerAlt is the same owner over https, which some taggers write.
// Accepted when reading, never written — the point is not to report drift against a
// file whose UFID is already correct in everything but the scheme.
const musicBrainzUFIDOwnerAlt = "https://musicbrainz.org"

// isMusicBrainzUFID reports whether a UFID frame is the MusicBrainz one. A file may
// carry several UFID frames from different taggers, each with its own owner, and the
// others are none of our business — they are left exactly as they are.
func isMusicBrainzUFID(owner string) bool {
	owner = strings.TrimSpace(owner)
	return strings.EqualFold(owner, musicBrainzUFIDOwner) ||
		strings.EqualFold(owner, musicBrainzUFIDOwnerAlt)
}

// SetMP3Tags writes ID3v2.4 metadata with github.com/bogem/id3v2. The returned
// changes are the field-level before/after actually applied; see models.TagChange.
//
// It used to shell out to `ffmpeg -codec copy`, which demuxed and remuxed the whole
// file to change a tag, could not address a frame directly, and made ffmpeg a runtime
// dependency for anyone with MP3s. Writing the frames ourselves also removes the
// ffprobe read that truncated a null-separated value to its first entry, which is what
// blocked the spec-correct multi-value form (see renderMP3Values).
//
// Every key in the change set must be written, including the ones whose desired value
// is empty (which the profile's remove_values turns into a change): the reported diff
// is derived from the change set, so a key reported but not written would be
// re-reported on every scan forever. An empty value deletes its frame.
func SetMP3Tags(filePath string, metadata models.FileTags, tagger models.TaggerSettings) (unchanged bool, tagsWritten int, changed []models.TagChange, err error) {
	desired := renderMP3Tags(buildMP3DesiredTags(metadata), tagger)

	existing, err := GetMP3Tags(filePath)
	if err != nil {
		return false, 0, nil, fmt.Errorf("read mp3 tags failed: %w", err)
	}

	changes, hasChanges := utilities.DiffID3Tags(existing, desired, tagger)
	if !hasChanges {
		logger.Log.Debug("no tag changes, returning")
		return true, 0, nil, nil
	}
	logger.Log.Debug("found tag changes")
	logger.Log.Debug(changes)

	tag, err := id3v2.Open(filePath, id3v2.Options{Parse: true})
	if err != nil {
		return false, 0, nil, fmt.Errorf("open mp3 for tagging failed: %w", err)
	}
	defer tag.Close()

	// A file already carrying a 2.3 tag would otherwise be written back as 2.3, which
	// has no TDRC/TDOR to put the dates in.
	tag.SetVersion(4)

	// The diff works in upper case; the frame descriptions have to keep the desired
	// map's own spelling ("originaldate", "MusicBrainz Album Id"), because that is
	// what is on disk and what other taggers look for.
	spellingOf := make(map[string]string, len(desired))
	for key := range desired {
		spellingOf[strings.ToUpper(key)] = key
	}
	valueOf := func(upperKey string) string {
		return encodeID3FrameValue(desired[spellingOf[upperKey]])
	}

	writeText := func(frameID, value string) {
		if value == "" {
			tag.DeleteFrames(frameID)
			return
		}
		tag.AddTextFrame(frameID, id3v2.EncodingUTF8, value)
	}
	writeUserDefined := func(description, value string) {
		if value == "" {
			deleteUserDefinedFrame(tag, description)
			return
		}
		tag.AddUserDefinedTextFrame(id3v2.UserDefinedTextFrame{
			Encoding:    id3v2.EncodingUTF8,
			Description: description,
			Value:       value,
		})
	}
	// UFID is keyed by its owner, not by a description, so replacing ours means
	// dropping only the MusicBrainz frame and putting every other tagger's back.
	writeUFID := func(value string) {
		deleteMusicBrainzUFIDFrame(tag)
		if value == "" {
			return
		}
		tag.AddUFIDFrame(id3v2.UFIDFrame{
			OwnerIdentifier: musicBrainzUFIDOwner,
			Identifier:      []byte(value),
		})
	}

	// The paired frames first: either half changing rewrites the whole frame, and
	// both being empty clears it.
	for frameID, halves := range id3PairedFrames {
		_, numberChanged := changes[halves[0]]
		_, totalChanged := changes[halves[1]]
		if !numberChanged && !totalChanged {
			continue
		}
		logger.Log.Trace("adding " + frameID)
		writeText(frameID, pairedFrameValue(valueOf(halves[0]), valueOf(halves[1])))
		if numberChanged {
			tagsWritten++
		}
		if totalChanged {
			tagsWritten++
		}
	}

	for upperKey := range changes {
		if isPairedHalf(upperKey) {
			continue // written above
		}
		logger.Log.Trace("adding " + upperKey)

		switch {
		case upperKey == ufidTagKey:
			writeUFID(valueOf(upperKey))
		default:
			if frameID, ok := id3TextFrameForKey[upperKey]; ok {
				writeText(frameID, valueOf(upperKey))
			} else {
				writeUserDefined(spellingOf[upperKey], valueOf(upperKey))
			}
		}
		tagsWritten++
	}

	// Every write is also the chance to retire the legacy ISRC frame, whether or not
	// the ISRC itself changed — a file whose ISRC is already correct would otherwise
	// keep the artefact forever, since nothing would ever mark it as drift.
	_, isrcChanged := changes["ISRC"]
	if migrateLegacyISRCFrame(tag, existing, isrcChanged) {
		tagsWritten++
	}

	// Same opportunity, for a frame that is not ours: a descriptionless TXXX is a value
	// with no key, which nothing can read and nothing can address. Counted like the
	// migration above — it is a change to the tag with no row in the diff, which is why
	// tagsWritten can exceed the reported change set.
	if dropped := dropUnkeyedUserDefinedFrames(tag); dropped > 0 {
		logger.Log.Debugf("dropped %d TXXX frame(s) carrying no description", dropped)
		tagsWritten += dropped
	}

	if err := tag.Save(); err != nil {
		return false, 0, nil, fmt.Errorf("id3 tag write failed: %w", err)
	}

	// Save reopens the file; release that handle before touching the tail. The
	// deferred Close above then no-ops on an already-closed file.
	_ = tag.Close()

	if removed, err := stripID3v1(filePath); err != nil {
		// The v2 tag — the one everything here reads — is already on disk, so a
		// failure to tidy the trailer is not a failure to tag the file.
		logger.Log.Warnf("could not strip the ID3v1 trailer from %s: %s", filePath, err.Error())
	} else if removed {
		logger.Log.Debug("removed a stale ID3v1 trailer")
	}

	// The diff is derived from the change set rather than from the write blocks: the
	// tag is saved in one pass, so there is no per-field success to report, and
	// `changes` is already exactly the set of fields that differed. tagsWritten can
	// exceed this count — a changed DISCNUMBER also rewrites its paired DISCTOTAL.
	changed = make([]models.TagChange, 0, len(changes))
	for key, values := range changes {
		// Described rather than joined, for the same reason as FLAC: with
		// mp3_multi_value_tags on, the change from a "; "-joined frame to a
		// null-separated one is invisible once both sides are joined back together.
		changed = append(changed, models.TagChange{
			Field: key,
			Old:   utilities.DescribeTagValues(existing[key]),
			New:   utilities.DescribeTagValues(values),
		})
	}
	utilities.SortTagChanges(changed)

	return false, tagsWritten, changed, nil
}

const (
	// id3v1TagSize is the whole of an ID3v1 tag: 128 bytes at the very end of the
	// file, opening with "TAG". The format has no length field — the size is the
	// definition.
	id3v1TagSize = 128
	// id3v1ExtendedTagSize is the "enhanced tag" some Winamp-era writers put
	// immediately *before* the 128-byte block, opening with "TAG+".
	id3v1ExtendedTagSize = 227
)

// stripID3v1 removes the ID3v1 trailer from a file that has just been retagged,
// reporting whether there was one. It is a truncation: ID3v1 lives at the end of the
// file and is not framed, so there is nothing to rewrite around.
//
// The trailer is removed rather than refreshed because nothing can keep it honest.
// The previous writer shelled out to ffmpeg with `-write_id3v1 1`, which rebuilt it on
// every write; bogem/id3v2 does not manage ID3v1 at all, so an existing one is copied
// through verbatim and now says whatever it said before Autotaggerr first saw the file.
// A 30-byte-truncated title and a genre from a fixed list of 80 cannot represent what
// gets written here anyway — it cannot hold an MBID, which is the thing every consumer
// in docs/tagging.md actually reads — so refreshing it was never on the table. That
// leaves keeping a tag that contradicts the file, or removing it; a file that disagrees
// with itself is the worse of the two, because a v1-only reader has no way to tell
// which half is current.
//
// Only called on a file being written anyway, so it costs nothing on the skip-unchanged
// path and cannot turn an untouched library into a rewritten one.
func stripID3v1(filePath string) (bool, error) {
	file, err := os.OpenFile(filePath, os.O_RDWR, 0)
	if err != nil {
		return false, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	size := info.Size()
	if size < id3v1TagSize {
		return false, nil
	}

	magic := make([]byte, 4)
	if _, err := file.ReadAt(magic[:3], size-id3v1TagSize); err != nil {
		return false, err
	}
	if string(magic[:3]) != "TAG" {
		return false, nil
	}
	size -= id3v1TagSize

	// The enhanced block only ever exists in front of a real ID3v1 tag, so it is
	// checked for only once one has been found. Leaving it behind would orphan a
	// structure whose header we just deleted.
	if size >= id3v1ExtendedTagSize {
		if _, err := file.ReadAt(magic, size-id3v1ExtendedTagSize); err != nil {
			return false, err
		}
		if string(magic) == "TAG+" {
			size -= id3v1ExtendedTagSize
		}
	}

	if err := file.Truncate(size); err != nil {
		return false, err
	}
	return true, file.Sync()
}

// isPairedHalf reports whether a key is one half of a paired frame, which is written
// with its other half rather than on its own.
func isPairedHalf(upperKey string) bool {
	for _, halves := range id3PairedFrames {
		if upperKey == halves[0] || upperKey == halves[1] {
			return true
		}
	}
	return false
}

// packISRCFrameValue applies the "KEY:value" packing the *legacy* ISRC frame carries.
// An empty ISRC packs to "", which deletes the frame rather than leaving "ISRC:"
// behind. Nothing writes this form any more — it is kept so tests can build a
// pre-migration file, which is the only way to prove the migration works.
func packISRCFrameValue(value string) string {
	if value == "" {
		return ""
	}
	return "ISRC:" + value
}

// deleteUserDefinedFrame removes the one TXXX frame with the given description.
// bogem can only delete a whole frame ID at once, so every other TXXX frame is read
// out first and put back — deleting the ID outright would take the MusicBrainz
// identifiers with it.
// migrateLegacyISRCFrame retires the TXXX artefact the ISRC used to live in, moving
// its value to TSRC when nothing else already has. It reports whether it did anything.
//
// The value is taken from `existing` — what was read off the file — rather than from
// the desired tags, because the case this exists for is the file whose ISRC is *not*
// changing. Reading the desired value would silently drop the ISRC of any file whose
// release no longer supplies one, turning a frame migration into a deletion.
//
// When the ISRC did change, the change set has already written TSRC with the new
// value and there is nothing to carry over — only the old frame to remove.
func migrateLegacyISRCFrame(tag *id3v2.Tag, existing map[string][]string, isrcChanged bool) bool {
	if !hasUserDefinedFrame(tag, legacyISRCFrameDescription) {
		return false
	}
	deleteUserDefinedFrame(tag, legacyISRCFrameDescription)
	if isrcChanged {
		return true
	}
	if value := encodeID3FrameValue(existing["ISRC"]); value != "" {
		tag.AddTextFrame("TSRC", id3v2.EncodingUTF8, value)
	}
	return true
}

// hasUserDefinedFrame reports whether a TXXX frame with this description is present,
// so a migration can tell "already done" from "nothing to do".
func hasUserDefinedFrame(tag *id3v2.Tag, description string) bool {
	for _, frame := range tag.GetFrames("TXXX") {
		if userDefined, ok := frame.(id3v2.UserDefinedTextFrame); ok &&
			strings.EqualFold(strings.TrimSpace(userDefined.Description), description) {
			return true
		}
	}
	return false
}

// deleteMusicBrainzUFIDFrame drops our UFID frame and keeps every other owner's. The
// frame ID is shared by every tagger that writes one, so deleting by ID alone would
// throw away identifiers that are not ours to remove.
func deleteMusicBrainzUFIDFrame(tag *id3v2.Tag) {
	kept := make([]id3v2.Framer, 0)
	for _, frame := range tag.GetFrames("UFID") {
		if ufid, ok := frame.(id3v2.UFIDFrame); ok && isMusicBrainzUFID(ufid.OwnerIdentifier) {
			continue
		}
		kept = append(kept, frame)
	}
	tag.DeleteFrames("UFID")
	for _, frame := range kept {
		tag.AddFrame("UFID", frame)
	}
}

// dropUnkeyedUserDefinedFrames removes TXXX frames that carry no description, and
// reports how many it dropped.
//
// A TXXX is a key/value pair whose *description* is the key, so one without a
// description names nothing. It is unreachable from both directions: GetMP3Tags trims
// the key, finds it empty and drops the value, so the frame is invisible to the diff;
// and deleteUserDefinedFrame matches by description, so nothing can ever target it.
// Invisible and immune, it would sit in the file forever.
//
// They are not hypothetical. A Windows Explorer property edit rewrites the whole ID3
// tag through the Windows property handler, and one file came back holding a TXXX whose
// description was empty and whose *value* was the text "MusicBrainz Recording Id" — the
// description shifted into the value slot and the MBID lost. Autotaggerr rewrote the
// MBID into a fresh frame beside it and could not touch the wreckage.
//
// Like stripID3v1, this runs only on a file that is already being rewritten for a real
// change: an unkeyed frame is not a diff and must never be the reason a file is written.
func dropUnkeyedUserDefinedFrames(tag *id3v2.Tag) int {
	frames := tag.GetFrames("TXXX")
	kept := make([]id3v2.Framer, 0, len(frames))
	for _, frame := range frames {
		if userDefined, ok := frame.(id3v2.UserDefinedTextFrame); ok &&
			strings.TrimSpace(userDefined.Description) == "" {
			continue
		}
		kept = append(kept, frame)
	}

	dropped := len(frames) - len(kept)
	if dropped == 0 {
		return 0
	}
	// A frame ID can only be deleted whole, so the survivors go back in — the same
	// dance deleteUserDefinedFrame does, and for the same reason.
	tag.DeleteFrames("TXXX")
	for _, frame := range kept {
		tag.AddFrame("TXXX", frame)
	}
	return dropped
}

func deleteUserDefinedFrame(tag *id3v2.Tag, description string) {
	kept := make([]id3v2.Framer, 0)
	for _, frame := range tag.GetFrames("TXXX") {
		if userDefined, ok := frame.(id3v2.UserDefinedTextFrame); ok &&
			strings.EqualFold(strings.TrimSpace(userDefined.Description), description) {
			continue
		}
		kept = append(kept, frame)
	}
	tag.DeleteFrames("TXXX")
	for _, frame := range kept {
		tag.AddFrame("TXXX", frame)
	}
}

// GetMP3Tags reads the ID3 frames back as the canonical keys the desired-tag map
// uses, so the two can be compared directly.
//
// It used to shell out to ffprobe, which flattens the tag into a string map — and
// flattens a null-separated multi-value frame to its first value on the way, which is
// what made the spec-correct representation unreachable rather than merely unwritten.
func GetMP3Tags(filePath string) (map[string][]string, error) {
	tag, err := id3v2.Open(filePath, id3v2.Options{Parse: true})
	if err != nil {
		return nil, fmt.Errorf("read mp3 tags failed: %w", err)
	}
	defer tag.Close()

	res := make(map[string][]string)
	// A frame payload may carry several values separated by a null byte (ID3v2.4's
	// own multi-value form). Splitting unconditionally is what keeps the reader
	// independent of the writer's setting: a library half-written in each form reads
	// correctly either way, and flipping the setting converges after one write.
	add := func(key, value string) {
		key = strings.TrimSpace(key)
		if key == "" {
			return
		}
		for _, part := range strings.Split(value, id3MultiValueSeparator) {
			if part = utilities.NormalizeTagValue(part); part != "" {
				res[key] = append(res[key], part)
			}
		}
	}

	for frameID, frames := range tag.AllFrames() {
		for _, frame := range frames {
			switch typed := frame.(type) {
			case id3v2.TextFrame:
				if halves, paired := id3PairedFrames[frameID]; paired {
					number, total := splitPairedFrameValue(typed.Text)
					add(halves[0], number)
					add(halves[1], total)
					continue
				}
				if key, known := id3KeyForTextFrame[frameID]; known {
					add(key, typed.Text)
				}
			case id3v2.UserDefinedTextFrame:
				key, value := decodeUserDefinedFrame(typed)
				add(key, value)
			case id3v2.UFIDFrame:
				// Only ours. Another tagger's UFID is a different identifier in a
				// different namespace, and folding it in here would diff our
				// recording MBID against a value that was never meant to be one.
				if isMusicBrainzUFID(typed.OwnerIdentifier) {
					add(ufidTagKey, string(typed.Identifier))
				}
			}
		}
	}
	return res, nil
}

// decodeUserDefinedFrame turns a TXXX frame into a canonical key and value. Normally
// the description *is* the key; the legacy ISRC frame is the exception whose value
// carries its own "KEY:value" packing (see legacyISRCFrameDescription). It still
// decodes to the same "ISRC" key the TSRC frame reads back as, so a file mid-migration
// and a file after it are indistinguishable to everything above this function.
//
// That packing is split on the FIRST colon only. The value may itself contain the
// separators used for multi-value tags — a "; "-joined ISRC list is routine on singles
// and featured tracks — and splitting on every colon truncated those to their first
// entry, so the tags never matched what was wanted and the file was rewritten on every
// scan. Regression test: TestMP3MultiISRCIdempotent.
func decodeUserDefinedFrame(frame id3v2.UserDefinedTextFrame) (key, value string) {
	description := strings.ToUpper(strings.TrimSpace(frame.Description))
	if description != legacyISRCFrameDescription {
		return description, frame.Value
	}
	packed := strings.SplitN(frame.Value, ":", 2)
	if len(packed) != 2 {
		return "", ""
	}
	return strings.ToUpper(strings.TrimSpace(packed[0])), packed[1]
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
