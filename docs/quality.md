# Feature: quality profiles

What quality an album is wanted in. "Wanted" ([collection.md](collection.md#the-desire-model))
says *which* albums you want. A quality profile says *how good* the copy has to be. Today it does
one visible thing, **Upgrade wanted**: a wanted album held below its profile's cutoff is flagged,
counted and filterable, the way Missing is.

It is also built as the decision engine that indexer search and grabbing will need later: "is this
release acceptable?", "is it better than what I hold?", "is what I hold good enough?". Those
questions are answered by one package, `quality/`, which knows nothing about the database or about
files. Today it judges files on disk. A future search must judge a release by its title with the
same rules (see [For grabbing](#for-grabbing)).

## What is taken from Lidarr, and what is not

**Taken, because it fits:**
- named quality levels, in Lidarr's spelling;
- a profile as an **ordered list of tiers**, where qualities in one tier rank as equally good
  (Lidarr's "groups");
- which tiers are **allowed**;
- a **cutoff**: the tier that is good enough;
- an **upgrades allowed** switch.

Someone coming from Lidarr reads the same words, and an indexer title saying `MP3-320` or
`FLAC 24bit` names the level it parses to.

**Not taken:**
- **A profile per artist.** Here the profile belongs to the **library**, because who decides is a
  property of the library ([collection.md](collection.md#authority-is-per-library)). An artist can
  override it. The resolution is described [below](#which-profile-applies).
- **Per-quality size limits (quality definitions), custom formats and scores, delay and release
  profiles.** These only matter when choosing among releases to grab, and they should be designed
  with grabbing, not before it.
- **Lidarr libraries.** Lidarr decides quality in its own libraries. The profile select is hidden
  for them, the API refuses one (400), and no album Lidarr governs is judged.

## The ladder

`quality.ID` values are stored, in library items and in profile tiers, so they never change. A new
codec is new rows (AAC/OGG, #23), never a renamed one. Worst first:

| ID | Name | Group |
|---|---|---|
| `unknown` | Unknown | — |
| `mp3-low` | MP3 (under 128) | lossy |
| `mp3-128` … `mp3-192` | MP3-128, MP3-160, MP3-192 | lossy |
| `mp3-vbr-v2` | MP3-VBR-V2 | lossy |
| `mp3-224`, `mp3-256` | MP3-224, MP3-256 | lossy |
| `mp3-vbr-v0` | MP3-VBR-V0 | lossy |
| `mp3-320` | MP3-320 | lossy |
| `flac` | FLAC | lossless |
| `flac-24` | FLAC 24bit | lossless |

`GET /quality` serves the ladder, so the UI's names have one definition.

### How a file is placed on it

`quality.Classify` takes what the headers say (`quality.Measured`) and **never guesses upward**:

- **FLAC**: a bit depth above 16 is `flac-24`, otherwise `flac`. No bit depth is Unknown.
- **CBR MP3**: the highest rung the frame header's bitrate reaches.
- **VBR MP3**: the LAME preset when the file states it. LAME writes `100 - 10·V - q` into the Xing
  header's quality field, so the preset is its tens digit, whatever algorithm quality `q` was used.
  V1 is placed at V2 (the ladder has no V1, and rounding down is the reading that never claims a
  cutoff the file does not meet). V3 and below fall back to their average bitrate. Without a
  preset (ffmpeg's muxer writes the field as 0), the average bitrate decides: 220 kbit/s and up is
  V0, 170 and up is V2, anything lower is placed as a CBR rate. Those bands sit below what each
  preset typically averages.
- Anything else is Unknown.

## Measuring files

`modules.ReadAudioProperties` reads **headers only**: FLAC's STREAMINFO, and an MP3's first frame
plus the Xing/Info or VBRI header it carries (VBR flag, frame and byte totals giving the average
bitrate, and the quality field). Organise already relied on it being a header read, and Process
now calls it for every file whose quality is not current, so it must stay one.

The result is stored on the library item: `quality`, `bitrate`, `sample_rate` and `bit_depth`,
stamped with `quality_size` / `quality_mod_time`. It is **current** while those still match the
item's size and mtime. That is the AudioHash rule: a tag write moves both, so the file is measured
again, but a header read is all that costs.

- **Process measures in `recordItem`** for every file it processes, whatever the attempt comes to.
- **A file skipped as unchanged is measured too** (`components.measureQualityIfStale`). Skipping
  never reaches `recordItem`, so without this a library indexed before quality existed would stay
  unmeasured until each file changed. Only the quality columns are written.
- **Unreadable audio is stored as `unknown`** at its size and mtime, so it is not read again every
  run. Unknown and unmeasured are never read as low quality (see [below](#upgrade-wanted)).

Items shows each file's quality (with bit depth, bitrate and sample rate on hover), and
Insights → Stats has a **Quality** breakdown, with "Not measured" for files not read yet.

## Profiles

`models.QualityProfile`: name, `tiers` (JSON: `[{qualities: [id…], allowed}]`, worst first),
`cutoff_tier` (an index into the tiers) and `upgrade_allowed`. `Profile()` hands the row to the
engine. `quality.Profile.Validate` refuses:

- an unknown or repeated quality;
- an empty tier;
- no allowed tier;
- a cutoff out of range or not itself allowed (a target you are not allowed to reach).

The API answers 400 with the reason.

**Three presets are created once** by the `seed_quality_profiles` data migration:

| Preset | Allows | Good enough at |
|---|---|---|
| **Lossless** | FLAC and FLAC 24bit, tied | FLAC |
| **High quality lossy** | V0, 320, FLAC, FLAC 24bit | MP3-320 |
| **Any** | everything but Unknown | the lowest rung, so it never asks for an upgrade |

They are a migration rather than a Seed step because Seed recreates what is missing on every
start, and deleting every quality profile is a valid choice.

**No library gets a profile on upgrade.** A library with none has no opinion, so nothing is
flagged. Upgrading the app changes nothing until someone picks a profile.

**Deleting a profile is never refused**, unlike a tagger profile. It clears the profile from every
library and artist using it, in one transaction. No profile is a valid state. The confirm dialog
names who uses it (`GET /quality-profiles` returns `libraries` and `artists`).

### Which profile applies

`collection.QualityIndex.QualityProfileFor(artist, library)` is the one definition, the way
`FollowWants` is for following. Every view goes through it, and a grabber will too:

1. none, unless Autotaggerr decides for this artist in this library (`LibraryIndex.Authority` is
   native: a detached artist's Lidarr library counts);
2. the artist's override (`collection_artists.quality_profile_id`), if it still exists;
3. the library's profile (`libraries.quality_profile_id`), if it still exists;
4. otherwise none.

## Upgrade wanted

**Facts are stored, policy is applied at read time.** The copies' qualities are read from the index
as they stand: a grouped query joining `collection_release_copies` to `library_items`, so only
files the collection counts decide an album. The profile is applied when the page is built. So
editing a profile, or an override, changes every page at once, with no rebuild. The cached
collection list notices because its fingerprint includes `quality_profiles` and `library_items`.

`collection.QualityIndex.JudgeAlbum` judges an album:

- **Within a library, the best edition decides.** An edition is good enough when every measured
  file of it meets the cutoff. The album is good enough when one edition is. This is the rule an
  album want is satisfied by, so owning a FLAC pressing and an MP3 one asks for nothing.
- **Unknown and unmeasured files are left out**, not read as failing. "Could not tell" is not "too
  low". An album with no measured file is not judged at all.
- **In the overview (every library)**, the verdict is the first library still asking for an
  upgrade, else the first one judged.

The album view (`releaseGroupView`) carries:
- `quality`: profile, lowest quality on the best edition, the cutoff's name, cutoff met, upgrades
  allowed. Absent when no profile applies or nothing was measured.
- `upgrade_wanted = wanted && owned && quality known && !cutoff met && upgrades allowed`.

Only **wanted** albums ask for an upgrade, as Lidarr's "cutoff unmet" covers only monitored ones,
and as Missing is gated on something deciding what is wanted. An owned album nobody wants is still
judged, and its quality is shown, but it is never flagged.

The views are built through `viewScope.quality`, which `artistInLibrary` loads for the albums on
the page. So the artist page, the discography, the album page and the MCP `get_artist` /
`get_album` tools all agree. The collection list loads it once for every album and counts
`upgrade_count` per artist, in either reading.

### In the UI

- **Collection**: an **Upgrade** chip (URL flag `upgrade=1`, beside Missing and Mismatched) and a
  sortable Upgrade column.
- **Artist page**: an **Upgrade** chip in the filter row. An owned album row shows its quality
  (`FLAC`, dim) or, when flagged, `↑ MP3-320 → FLAC` (held → good enough), with the profile in the
  tooltip. A **Quality** card under the follow settings sets the artist's override ("Each library's
  own" or a profile, and says which profile each native library uses). It is absent when none of
  the artist's libraries is native.
- **Libraries**: a Quality profile select on the form, hidden for a Lidarr-managed library.
- **Setup → Quality profiles**: the list (accepts, good enough at, upgrades, used by), plus add,
  edit and delete. The editor lists qualities best first, with up/down to rank, **Tie with below**
  and **Untie** for equal tiers, Allowed checkboxes, a cutoff select of allowed tiers, and a summary
  sentence of what the choices add up to. It tracks the cutoff by quality rather than by index, so
  the cutoff follows its tier through every move.
- **MCP**: `list_artists` rows carry `upgrade` and take `filter: "upgrade"`. Album rows carry
  `quality`, `quality_cutoff` and `upgrade_wanted`.

## API

| Route | Does |
|---|---|
| `GET /quality` | the ladder |
| `GET /quality-profiles` | profiles, with the libraries and artist count using each |
| `POST /quality-profiles` | create. No `tiers` starts from the whole ladder, nothing allowed, so a name alone is refused |
| `GET/PUT/DELETE /quality-profiles/:id` | read, edit (validated), delete (clears references) |
| `PUT /artists/:mbid/quality-profile` | `{quality_profile_id: id \| null}`: the override, or back to each library's |
| `PUT /libraries/:id` | `quality_profile_id` (clearable). Refused for a Lidarr library, judged by the manager after the edit |

Like tagger profiles, these are open to any signed-in user, not admin-only.

## For grabbing

When Autotaggerr searches indexers, the search must not grow its own idea of quality. The contract:

- **Parse a release title into a `quality.ID`** with a `quality.ParseTitle`, still to be written.
  It goes in the `quality` package beside `Classify`, and turns "FLAC 24bit", "V0" or "320" into
  the same rungs. This is where Lidarr's title parsing earns a look.
- **Ask the same profile** the album is judged by: `QualityProfileFor(artist, library)`, for the
  library the want points into.
- **Decide with the engine:** `Allowed(candidate)` to consider a release at all,
  `IsUpgrade(held, candidate)` to replace what is held (upgrades on, candidate allowed and ranked
  higher, held below the cutoff: nothing is an upgrade once the cutoff is met), and
  `AlbumCutoff(held…)` / `JudgeAlbum` to know whether to search at all.
- **What is deliberately left for then:** size limits per quality, preferring one release among
  several of equal quality (custom formats or scores), delays, and an import gate or auto-replace
  driven by the profile. Import's Replace decision is still a person's consent today
  ([downloads.md](downloads.md#auto-import)).

## Tests

- `quality/quality_test.go`: classification (CBR, VBR by preset and by bitrate, FLAC 16/24),
  ranking with tied tiers, allowed, cutoff, upgrade, album cutoff with unmeasured files,
  validation, the presets, the JSON round trip.
- `modules/naming_file_test.go`: header reads on ffmpeg-made CBR/VBR MP3s and FLAC 16/24, the Xing
  quality field, and a VBRI frame.
- `components/quality_test.go`: Process measures, a skipped file is measured without being
  re-processed, unreadable audio is stored once.
- `collection/quality_test.go`: profile resolution (override, library, none, Lidarr, dangling),
  best edition, the overview's verdict, scoping to the albums named.
- `routers/quality_profiles_test.go`: CRUD, a Lidarr library refusing a profile, and upgrade wanted
  end to end, including the override taking effect with no rebuild and the cached list noticing.
- `database/migrate_test.go`: the presets are seeded once, and stay deleted.
