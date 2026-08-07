package modules

import (
	"errors"
	"fmt"
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
// models.ConfigStruct.AutotaggerrMP3MultiValueTags.
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
func renderMP3Tags(desired map[string][]string, configFile models.ConfigStruct) map[string][]string {
	rendered := make(map[string][]string, len(desired))
	for key, values := range desired {
		rendered[key] = renderMP3Values(values, configFile.AutotaggerrMP3MultiValueTags)
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
		// ISRC is written as a TXXX:ISRC frame (see below) and decoded by GetMP3Tags.
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
		// Picard additionally writes a UFID frame, which is reachable now that the
		// writer addresses frames directly but is deliberately not written yet — see
		// docs/wip.md.
		"MusicBrainz Recording Id": single(metadata.MBRecordingID),
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

// isrcFrameDescription is the description of the TXXX frame the ISRC lives in.
//
// It is not "ISRC" but the literal string "TXXX", which is an artefact: the previous
// writer passed `-metadata TXXX=ISRC:<value>` to ffmpeg, which stored a user-defined
// frame *described* "TXXX" whose value carries its own "KEY:value" packing. The
// spelling is preserved deliberately — changing it would re-tag every MP3 in every
// library for no gain, and moving the ISRC to the standard TSRC frame is a separate,
// deliberate change (see docs/wip.md).
const isrcFrameDescription = "TXXX"

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
func SetMP3Tags(filePath string, metadata models.FileTags, configFile models.ConfigStruct) (unchanged bool, tagsWritten int, changed []models.TagChange, err error) {
	desired := renderMP3Tags(buildMP3DesiredTags(metadata), configFile)

	existing, err := GetMP3Tags(filePath)
	if err != nil {
		return false, 0, nil, fmt.Errorf("read mp3 tags failed: %w", err)
	}

	changes, hasChanges := utilities.DiffID3Tags(existing, desired, configFile)
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
		case upperKey == "ISRC":
			writeUserDefined(isrcFrameDescription, packISRCFrameValue(valueOf(upperKey)))
		default:
			if frameID, ok := id3TextFrameForKey[upperKey]; ok {
				writeText(frameID, valueOf(upperKey))
			} else {
				writeUserDefined(spellingOf[upperKey], valueOf(upperKey))
			}
		}
		tagsWritten++
	}

	if err := tag.Save(); err != nil {
		return false, 0, nil, fmt.Errorf("id3 tag write failed: %w", err)
	}

	// The diff is derived from the change set rather than from the write blocks: the
	// tag is saved in one pass, so there is no per-field success to report, and
	// `changes` is already exactly the set of fields that differed. tagsWritten can
	// exceed this count — a changed DISCNUMBER also rewrites its paired DISCTOTAL.
	changed = make([]models.TagChange, 0, len(changes))
	for key, values := range changes {
		changed = append(changed, models.TagChange{
			Field: key,
			Old:   utilities.JoinTagValues(existing[key]),
			New:   utilities.JoinTagValues(values),
		})
	}
	utilities.SortTagChanges(changed)

	return false, tagsWritten, changed, nil
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

// packISRCFrameValue applies the "KEY:value" packing the ISRC frame carries. An empty
// ISRC packs to "", which deletes the frame rather than leaving "ISRC:" behind.
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
			}
		}
	}
	return res, nil
}

// decodeUserDefinedFrame turns a TXXX frame into a canonical key and value. Normally
// the description *is* the key; the ISRC frame is the exception whose value carries
// its own "KEY:value" packing (see isrcFrameDescription).
//
// That packing is split on the FIRST colon only. The value may itself contain the
// separators used for multi-value tags — a "; "-joined ISRC list is routine on singles
// and featured tracks — and splitting on every colon truncated those to their first
// entry, so the tags never matched what was wanted and the file was rewritten on every
// scan. Regression test: TestMP3MultiISRCIdempotent.
func decodeUserDefinedFrame(frame id3v2.UserDefinedTextFrame) (key, value string) {
	description := strings.ToUpper(strings.TrimSpace(frame.Description))
	if description != isrcFrameDescription {
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
