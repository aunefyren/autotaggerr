# Feature: file import

Loose files on the server into a library — the last piece of the native manager (M6 pass E).
*Import* (admin-only) scans a folder, offers its albums, and for each one asks what folder attach
asks: which release, and which track each file is. What it adds is **where each file goes** — the
library's naming templates ([naming.md](naming.md)) — and **what is in the way**. What lands is a
pinned, tagged library item, exactly what a manual attach would have produced had the file
already been in the library.

## The flow

1. **Scan a folder** (`GET /import/scan?path=`). `modules.ScanImportSource` walks it and groups
   audio files by album folder, folding a disc folder (`CD 02`) into the album above it by the
   shared recogniser. Hidden folders (`.stfolder`, `.@__thumb`) are skipped. The scan reads at most
   5,000 files; past that, the answer is a narrower path. The scanned path goes in the page URL
   (`?path=`, written with `replace`), so Back and refresh find the scan again, and the last five
   are offered as **Recent** chips kept in the browser only (`localStorage`, every access guarded —
   a convenience for the person at the screen, never state anyone else reads).
2. **Identify** — step one of the modal. Each group carries a hint, read in order of trust
   (`modules.importHint`), and says where each part came from (`artist_from`, `album_from`,
   `year_from`), which the modal states as *The files say*:
   1. **the files' own tags** — the value most files agree on, `ALBUMARTIST` before `ARTIST`, the
      original date before the date; a download is nearly always tagged;
   2. **the album folder's own name** (`ParseAlbumFolderName`): `Artist - Album (Year) [FLAC]`,
      `Artist - Year - Album`, `Artist.Album.Year.WEB`, `Artist-Album-WEB-2014-GROUP`;
   3. **the parent folder, only inside the scanned root.** That is the library layout's rule, right
      only for a tree laid out as `<root>/Artist/Album`. It used to apply everywhere, so an album
      directly under the root was searched for by whatever folder the downloads lived in —
      `config`, `complete`, `downloads`. `TestImportHintNeverReadsTheArtistFromAboveTheRoot`.

   A group whose tags all name one release skips this step and opens on the review; Identify is
   one step back. Otherwise there are two tabs:
   - **Browse** (the default): artist → album → edition, each column narrowing the next. Artists
     come from `/search/artists` with MusicBrainz's description, so same-named artists can be told
     apart; albums from `GET /search/artists/:mbid/release-groups` (any MusicBrainz artist — an
     import is how an artist the collection has never seen arrives), grouped by primary type, the
     one the hint names marked; editions from `/release-groups/:mbid/releases`, each with its
     formats and **track count set against the number of files**. *Only editions with N tracks* is
     on by default and says how many it hid — the count is the one fact that separates the six-track
     CD from the box set of the same album. An exact artist name match and an exact album title
     match are selected for you; the edition never is, because that is the choice being made.
     A **Selected** line states the pick before *Review mapping*.
   - **Search all releases**: the fielded search folder attach uses, prefilled from the hint, which
     also takes a pasted musicbrainz.org link.
3. **Review** — step two (`POST /import/preview`). Given sources, the server maps them with
   `MapFilesToTracks` (the folder attach mapper); given rows — the table after someone changed a
   track or ticked Replace — it plans those as they are. Every edit goes back to the server, which
   owns the target path and the conflicts; the table never computes either. The chosen release
   heads the step with **Change release**, and **← Back** (or the finished *Identify* step) returns
   to Identify exactly as it was left: the step stays mounted, hidden, under the review.
4. **Import** (`POST /import`) queues a file-writing job. Move (the default) or Copy is chosen in
   the review.

## Where the source may be

A typed absolute path — the field completes folders as it is typed (`GET /import/folders`,
`modules.SuggestFolders`: children after a separator, case-insensitive prefix matches otherwise, at
most 50, hidden folders only when asked for with a leading dot; library folders are listed but
marked) — checked by `routers.importRoot`: it must exist, and it may be **neither
inside a library nor contain one**. Inside a library its files are already indexed, and moving
them is the job of an *Organise* verb, not an import; containing one would offer the library's own
files for import. Every source in a request must lie under that root.

The routes are admin-only (`auth.RequireAdmin`), for the reason settings are: the request names a
path anywhere on the server, and the job moves and deletes files there.

## Which libraries can take one

Enabled, mounted, and **not managed by Lidarr** (409). Lidarr decides identity there — the same
gate as attach, `libraryIdentityEditable` — and it tracks files by path, so a file placed in its
folders by anything else is a file it does not know. The page lists such libraries disabled, with
the reason, rather than leaving them out.

## Conflicts, and what Replace means

`process.PlanImport` marks each row with what stands in its way:

| Conflict | Meaning | Resolution |
|---|---|---|
| `exists` | a file is already at the target path | Replace, or skip |
| `duplicate` | the library already indexes this track *of this edition* at another path — an MP3 being upgraded to FLAC | Replace, or skip |
| `clash` | two files of this import render one target | fix the mapping or skip one; Replace cannot fix it |

Clash detection ignores case, so two targets that differ only in case on a case-insensitive
filesystem are caught; over-reporting costs a skip, under-reporting costs a file. A duplicate is
the same *release* track only: a different edition of the same recording is a legitimate second
copy, not a conflict.

**Replace is per row consent**, a checkbox in `--danger-text` whose `title` lists every file it
deletes, and `CheckImportPlan` refuses any conflict without it. The review table is the
confirmation (the style guide's *Review step*), so there is no second dialog.

**The plan is made twice.** The job re-plans when it runs, because the library may have changed
while the import sat in the queue, and refuses a row whose replacements include anything that was
not in the plan the reviewer agreed to (`importRowReady`): a replacement is only ever of something
someone saw.

## The job

`Runner.importNow`, one file at a time under the queue's job lock, as job kind `import`
(file-writing, so it is ordered ahead of pending metadata passes):

1. **Transfer** (`modules.TransferFile`). A move is a rename; when that fails (crossing
   filesystems, typically) it becomes copy-then-delete. A copy is written beside the target under a
   temporary name, synced, and renamed into place, so a failed copy never leaves a half-written
   file at the target or costs the file it was replacing. **There is no hardlink mode**: the file is
   tagged once it lands, and tagging a hardlink rewrites the other name's file too — a seeding
   torrent's, typically.
2. **Remove what it replaced**, only after the new file is in place: other files are deleted with
   their index and tempo rows; the row at the target itself is re-used, so it keeps its ID.
3. **Index** the file pinned to the chosen track, with its audio hash recorded
   (`PinnedAudioHash`) — the same row `saveCorrelation` writes for an attach.
4. **Tag** it through `retagItem`. A file whose tags fail is still imported and indexed with no
   processed version, so the next Process tags it; moving it back would be the worse surprise.

Then, after a move, folders the move left empty are removed up to — never including — the scanned
root (`modules.RemoveEmptyDirs`). A folder still holding anything (a cover, a cue sheet, a log) is
left alone, which is what keeps this from deleting what the import did not take. The collection is
re-derived for the album's artist (the release-group's credit, the same authority the naming uses)
and the albums whose tags changed go to Plex.

The **Activity event** is `file_import`. Each row's `path` is where the file now is and `from`
(`EventItem.From`) where it came from; counters are files imported, replaced and failed. A stopped
job stops between files, never inside a transfer.

## Known limits

- **Extra files are not imported.** Covers, cue sheets and logs stay in the source folder (which is
  why it is then not removed). Lidarr's *import extra files* is the model if this is wanted.
- **Plex learns of a new album by its own scan.** The Plex hand-off refreshes albums Plex already
  has; a brand-new album needs Plex's library scan (or its folder watcher) to appear.
- **No AcoustID suggestion yet.** Identification is the release in the tags, or the search.

## Tests

`modules/import_test.go` (grouping, the agreed release ID against real tagged FLACs, transfer and
its atomic replace, empty-folder cleanup, `PathWithin`), `process/import_test.go` (move, tag, pin
and event end to end; copy; replace only with consent, re-using the target's row; clash; the
unreviewed-replacement guard), `routers/import_test.go` (admin gate, source validation, the Lidarr
refusal, preview and the 400s).

## Related

- [downloads.md](downloads.md) — finished downloads from a download client, offered for import.
- [naming.md](naming.md) — where a file goes.
- [attach.md](attach.md) — the identification it reuses, and pins.
- [scanning.md](scanning.md#the-job-queue) — the queue the job runs on.
