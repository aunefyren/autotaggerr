# Work in progress

Living document for what is **not done yet**: roadmap, ideas, known issues, in-flight work. Add
items as they come up; when one ships, move what is worth keeping into its feature doc and delete
the rest from here.

Shipped features are documented in [media-manager.md](media-manager.md),
[collection.md](collection.md), [attach.md](attach.md), [scanning.md](scanning.md),
[tagging.md](tagging.md), [fingerprinting.md](fingerprinting.md) and
[authentication.md](authentication.md).

## Needs live verification

Built and unit-tested, never run against real data. Listed because a session of live testing has
repeatedly found faults no test could — the UI claiming a state the data did not hold.

- **Bulk attach** (folder select → review → attach). Watch the mapper against your own file-naming
  conventions; that is where the assumptions are.
- **Collection authoring** — adding an artist you own nothing of, following, per-album and
  per-edition wants, track selection under both "any release" and a specific edition.
- **Per-edition ownership** — needs a rebuild before any of it appears, and a reissue you own two
  pressings of to be meaningful at all.
- **AcoustID identification** — needs a real client key from acoustid.org, plus `fpcalc` (already in
  the Docker image; `libchromaprint-tools` on Ubuntu).
- **OIDC login** — never tested against a real identity provider. The flow is now covered end to
  end against a *fake* one (`auth/oidc_flow_test.go` for `StartLogin`/`CompleteLogin`,
  `routers/oidc_flow_test.go` for the two redirects): real discovery document, real JWKS, real RS256
  ID token, plus the refusals (state mismatch, forged flow cookie, replayed nonce, wrong audience,
  expired token, unverified email). That retires the "is the plumbing wired" question. What a fake
  cannot predict is where real issuers differ — a trailing slash in the issuer, `email_verified`
  sent as a string, group claims, clock skew — so a session against an actual provider is still
  worth having.
- **Artwork on a real collection.** The Cover Art Archive path was verified end to end against one
  real release-group (fetched, cached, served, cache-hit on the second call), but never against a
  browsing page with hundreds of rows. What to watch: first-paint behaviour on a cold cache, how many
  rows resolve to no cover at all, and whether `config/artwork/` grows to a size worth pruning.
- **fanart.tv artist images** — needs a personal API key from fanart.tv, added as a `fanart` data
  source. Entirely untested against the real service: the thumb/backdrop resolution is covered only
  by a stubbed response. Without a key, artists show monogram tiles, which *is* the tested path.
- **The reworked browsing pages** (collection / artist / release-group). Rebuilt around artwork,
  coverage meters, sortable-filterable tables and grouped catalogue sections; the release-group page
  lost its three scope buttons in favour of checkboxes on the editions and tracks themselves. Worth a
  session with a real library: whether the Albums/EPs/Singles/Other split puts things where you expect,
  and whether ticking editions and tracks records the want you meant.

## Open work

- **M6 pass E — file import.** Move/copy loose files into the library layout, then hand off to
  manual attach. The last unbuilt piece of the native manager.
- **Follow has no date cutoff.** "Only future releases" is not implemented, so following always
  pulls the whole back catalogue of the chosen types. A global follow default could layer on later
  without a schema change.
- **Drift sync has no schedule of its own.** It runs on demand only; it should get its own cron
  entry rather than riding the scan.
- **More activity events** — Plex refresh, health checks, file import. A per-file `tag_write` event
  could reuse the tag-diff component. Retention is a fixed 200 events; time-based retention could be
  configurable.
- **Surface *which* fields changed** in a drift sync, not just that a release changed.

## Roadmap / ideas

- **Additional audio formats** (OGG, M4A/AAC, …). Tagging covers FLAC (`metaflac`) and MP3
  (`ffmpeg`) only.
- **More MusicBrainz fields** written per track.
- **Write/normalize NFO sidecars** (`album.nfo` / `artist.nfo`). Autotaggerr already holds the full
  MusicBrainz release + artist data while tagging, so it could emit consistent sidecars (single
  `<albumartist>` + MB IDs) for NFO-first players like Jellyfin/Kodi. Open design questions:
  overwrite vs. merge vs. create-if-absent (Jellyfin-generated NFOs carry extra data like
  `<lockdata>`, `<dateadded>`, artwork paths, AudioDB IDs); Kodi-plain vs. Emby/Jellyfin dialect;
  only useful if Jellyfin's NFO *saver* is off, otherwise it rewrites the file. Would fix the
  duplicate-artist issue below at the source.

## Known issues / limitations

- **`correlation_source` does not refresh when the manager changes.** Swapping or disabling a
  library's manager and rescanning leaves items showing the old source, because skip-unchanged
  (status ok + unchanged size/mtime + same app version) walks past them without re-correlating.
  Working as designed for scan speed, but surprising: observed as `/items` still reporting `lidarr`
  after the Lidarr manager was taken out of the loop. Consider treating a manager change as a
  re-process trigger, the way an app-version change already is.

- **Cold-cache scans are slow.** A large personal library can take ~7 hours. The MusicBrainz
  1 req/s limiter is global, so a cold scan is floored by it no matter how many workers run; the
  caching and concurrency work (see [scanning.md](scanning.md)) mainly helps the warm-cache steady
  state.
  **Next idea:** measure the real warm-scan speedup and tune the default worker count; consider a
  separate, higher cap for MP3s than for FLAC, since FLAC rewrites are more disk-bound.

- **Jellyfin duplicate artists via NFO / online providers.** Jellyfin keys artist identity on the
  *name string*, not the MB ID (which Autotaggerr does tag). When an online provider like TheAudioDB
  spells an artist differently from MusicBrainz (e.g. straight `'` vs curly `’` apostrophe),
  Jellyfin creates two artists and can persist both into `album.nfo` as repeated `<albumartist>`
  lines. Autotaggerr's tags are correct — they mirror MusicBrainz — and the split originates in
  Jellyfin. Workaround: disable the NFO reader and/or TheAudioDB in Jellyfin so it trusts the
  embedded tags. See the NFO-sidecar idea above for a possible in-app fix.
