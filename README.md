# Autotaggerr

[![CI](https://img.shields.io/github/actions/workflow/status/aunefyren/autotaggerr/go.yml?branch=main&style=for-the-badge&label=CI)](https://github.com/aunefyren/autotaggerr/actions/workflows/go.yml)
[![Coverage](https://img.shields.io/endpoint?url=https://gist.githubusercontent.com/aunefyren/111899f93369f8f73ab6a4296d23da9a/raw/autotaggerr-coverage.json&style=for-the-badge)](https://github.com/aunefyren/autotaggerr/actions/workflows/go.yml)

Autotaggerr is a self-hosted music library manager that tags your files with the full
[MusicBrainz](https://musicbrainz.org/) metadata and keeps them that way. It runs as a server with a
web UI, works through your library on a schedule, and follows MusicBrainz as the data changes.

If you have used [Picard](https://picard.musicbrainz.org/), think of it as Picard running on your
server instead of your desktop. If you have used [Lidarr](https://lidarr.audio/), think of it as a
library manager that writes proper metadata into your files, and leaves downloading to you.

> [!WARNING]
> There is currently no official release of this software. It is in beta and has not been
> advertised. Get in contact with me if you are interested in it.

![Demo video](https://raw.githubusercontent.com/aunefyren/autotaggerr/refs/heads/main/.github/assets/demo.gif)

*Featuring artists, credited the way MusicBrainz credits them, as they show up in Plexamp.*

---

## Why it exists

**Picard** is the best tagger there is, but it is a desktop application. You open it, load a batch,
check the matches and save. A library on a server keeps changing: new albums arrive, MusicBrainz
editors fix credits, releases get merged. Keeping that tagged means going back to Picard again and
again. Autotaggerr writes the same fields Picard does, using Picard's tag mapping, but does it
unattended and keeps doing it.

**Lidarr** is good at keeping a music library organised, but the tags it writes are thin. Featured
artists, multiple album artists, composers, producers and performers are left out, and MP3s get no
MusicBrainz IDs at all. Autotaggerr has its own library manager, so you can follow artists, see what
you are missing, import new music and keep files named consistently, with complete tags on every
file.

**Already running Lidarr?** Autotaggerr can also sit next to it. Lidarr keeps deciding which release
a file is, and Autotaggerr writes the tags. See [Using it with Lidarr](#using-it-with-lidarr).

### Compared

| | Picard | Lidarr | Autotaggerr |
|---|---|---|---|
| Runs on a server, on a schedule | No | Yes | Yes |
| Full MusicBrainz tags (credits, relationships, sort names) | Yes | No | Yes |
| MusicBrainz IDs in MP3s | Yes | No | Yes |
| Re-tags when MusicBrainz data changes | No | No | Yes |
| Follow artists, track wanted albums | No | Yes | Yes |
| Rename and move files by template | Yes | Yes | Yes |
| Imports finished downloads from a download client | No | Yes | Yes |
| Searches indexers and starts downloads | No | Yes | No |

Downloading is out of scope on purpose. Autotaggerr can watch a download client for finished
downloads, but it never searches for or grabs anything.

---

## Features

### Tagging

- **Complete MusicBrainz metadata.** Artists and album artists with their join phrases (`feat.`,
  `&`, `x`), sort names, release dates and original dates, labels, catalogue numbers, release type,
  genres, ISRCs, disc subtitles and all MusicBrainz IDs.
- **Credits from relationships.** Composer, lyricist, writer, arranger, conductor, producer,
  engineer, mixer, remixer and performers with their instruments, read from the release, recording
  and work relationships. This is Picard with "Use track and release relationships" turned on.
- **FLAC and MP3.** FLAC through `metaflac`, MP3 (ID3v2.4) in-process with nothing to install.
- **Diff before write.** Files whose tags are already correct are not touched, and every change is
  listed per file in the Activity feed.
- **Tagger profiles.** Per-library settings for how tags are written, including adjustments for
  what Plex and Jellyfin can actually read. See [docs/tagging.md](docs/tagging.md).
- **Tempo and loudness** *(optional)*. BPM from the audio with [aubio](https://aubio.org) and
  ReplayGain 2.0 with [rsgain](https://github.com/complexlogic/rsgain), opted into per library.

### Identifying files

- **From the tags already there.** A library tagged by Picard (or anything else that writes
  MusicBrainz IDs) is identified straight away.
- **By hand, once.** Search MusicBrainz for the release, pick the track, and the file is tagged and
  pinned. Since the file now carries its IDs, it identifies itself from then on.
- **By fingerprint** *(optional)*. [AcoustID](https://acoustid.org) suggests matches for untagged
  files. It never tags on its own; you confirm the track.
- **From Lidarr**, when a library is managed by it.

### Managing the library

- **Collection view.** Artists, albums and editions you own, and which ones you are missing.
- **Follow artists** and track the albums you want, computed from MusicBrainz.
- **Import.** Point it at a folder, or let it watch qBittorrent, Transmission or SABnzbd for
  finished downloads. Each album is identified and reviewed before it is moved into the library.
- **Naming templates and Organise.** Lidarr-style tokens for artist, album, disc and track names.
  Organise shows a dry run of every move before anything is renamed. See
  [docs/naming.md](docs/naming.md).
- **A local MusicBrainz mirror.** Everything your collection refers to is cached in the database and
  refreshed nightly, so browsing and re-tagging do not hit the network. Merged and deleted
  MusicBrainz entities are detected and repaired. You can also point it at your own MusicBrainz
  mirror and lift the 1 request per second limit.
- **Artwork.** Covers from the [Cover Art Archive](https://coverartarchive.org) with no setup.
  Artist images from fanart.tv if you add your own free API key. Everything is proxied and cached
  locally.

### Running as a server

- Web UI with users, password and OIDC login, and API keys.
- A serial job queue, scheduled runs, and an Activity feed showing what each run changed.
- Tells Plex to refresh only the albums that changed, on one or more servers.
- An [MCP](https://modelcontextprotocol.io) endpoint at `/api/v1/mcp`, so an AI assistant can query
  your collection, and with a read and write key follow artists, identify files or start runs. See
  [docs/mcp.md](docs/mcp.md).
- Storage overview of your libraries and disks.
- Single Docker image with every optional tool included.

---

## Getting started

### Docker Compose

```yaml
services:
  autotaggerr:
    container_name: autotaggerr-app
    image: ghcr.io/aunefyren/autotaggerr:beta
    restart: unless-stopped
    volumes:
      - ./data/:/app/config/:rw               # Config, database and caches
      - /media/library/music/:/music/:rw      # Your music library
    environment:
      # These override config.json settings
      PORT: 8080
      TZ: Europe/Paris
      PUID: 1000
      PGID: 1000
```

Open the web UI on port 8080. Libraries, managers, data sources, media servers and tagger profiles
are all set up there.

### Without Docker

Autotaggerr is a single Go binary. One external tool is required:

- [`metaflac`](https://xiph.org/flac/download.html), for writing FLAC tags.
  `sudo apt install flac` on Debian/Ubuntu, `choco install flac` on Windows.

Optional tools turn on optional features. Without them the feature reports itself as unavailable
and nothing else changes.

| Tool | Feature | Debian/Ubuntu | Alpine | macOS |
|---|---|---|---|---|
| `fpcalc` ([chromaprint](https://acoustid.org/chromaprint)) | AcoustID fingerprinting | `apt install libchromaprint-tools` | `apk add chromaprint` | `brew install chromaprint` |
| `aubiotrack` ([aubio](https://aubio.org)) | Tempo (BPM) | `apt install aubio-tools` | `apk add aubio` | `brew install aubio` |
| [`rsgain`](https://github.com/complexlogic/rsgain) | ReplayGain | `apt install rsgain` | `apk add rsgain` | `brew install rsgain` |

To process a single file and exit:

```bash
autotaggerr --file "/music/Artist/Album (2020)/01 Track.flac" --fileRoot "/music"
```

### Library layout

New files are placed by the library's naming templates, which default to
`<root>/<Artist>/<Album> (<Year>)/[CD 01]/<01 Track>`. Identifying a file does not depend on that
layout, so an existing library does not need to be moved first. `--fileRoot` and a library's path
are the folder that contains the artist folders.

---

## Using it with Lidarr

Lidarr can be added as a **manager** for a library. Lidarr then decides which MusicBrainz release
each file belongs to, which matters for MP3s since Lidarr does not write those IDs into them.
Autotaggerr follows Lidarr's history, so new imports and upgrades are tagged within minutes, and
mirrors Lidarr's wanted list into the collection view.

Lidarr writes tags too. With the wrong settings the two will keep overwriting each other. In Lidarr,
go to **Settings → Metadata**:

- Set *Tag Audio Files with Metadata* to *For new downloads only*. Lidarr tags a file once on
  import, and Autotaggerr owns it after that. *All files* and *Sync* rewrite existing files; the
  health check and the manager's **Test** button warn about both.
- Uncheck *Scrub Existing Tags*.

Libraries without a manager, or with the native one, do not need Lidarr at all.

---

## Media server notes

**Plex**

- Enable *Prefer local metadata*: Library → Manage library → Edit → Advanced.
- Plex shows one album artist, and renders a joined value as one artist literally named `A; B`.
  `ALBUMARTIST` therefore holds the primary artist, and the full credit goes in `ALBUMARTISTS`, which
  Navidrome and Picard read.
- Plex reads MP3 tags through ffmpeg, which only sees the first value of a multi-value tag. MP3
  genres are joined with `; ` unless the tagger profile's `mp3_multi_value_tags` is on. FLAC always
  uses proper multi-value tags.
- Record Label holds one value. `single_record_label` writes only the first label.
- The album year comes from `DATE`, which is the edition's. Turn on `original_date_as_date` to show
  a remaster under the year the album first came out (this applies to Jellyfin too).

**Jellyfin**

- Enable the library's *Prefer non-standard artists tag* option to read the full `ALBUMARTISTS` and
  `ARTISTS` credits.
- Jellyfin identifies artists by name. If an online provider such as TheAudioDB spells a name
  differently from MusicBrainz (a straight `'` against a curly `’`), you can end up with duplicate
  artists, sometimes saved into `album.nfo`. Disabling the `Nfo` metadata reader (and possibly
  TheAudioDB) for the music library makes Jellyfin trust the embedded tags: Dashboard → Libraries →
  your music library.

---

## Configuration

Most things are configured in the web UI. Libraries, managers, data sources, download clients, media
servers and tagger profiles are stored in the database, not in a config file. Server settings live
in `config/config.json`, which is created on first start and can be edited from **Settings** as an
admin. Schedules, log level, concurrency and the mirror switch apply immediately; the rest apply at
the next start. See [docs/settings.md](docs/settings.md).

A setting can be overridden with a startup flag or environment variable. Precedence is
**startup flag → environment variable → config file**, and a flag or variable only overrides when
it is set.

<details>
<summary>Example <code>config.json</code></summary>

```json
{
	"autotaggerr_artwork_cron_schedule": "0 0 5 * * *",
	"autotaggerr_artwork_disabled": false,
	"autotaggerr_download_poll_cron_schedule": "0 */2 * * * *",
	"autotaggerr_environment": "prod",
	"autotaggerr_event_detail_retention": 500,
	"autotaggerr_event_retention": 200,
	"autotaggerr_event_retention_days": 0,
	"autotaggerr_external_url": "",
	"autotaggerr_health_cron_schedule": "0 */5 * * * *",
	"autotaggerr_lidarr_follow_cron_schedule": "0 */10 * * * *",
	"autotaggerr_lidarr_follow_disabled": false,
	"autotaggerr_log_level": "info",
	"autotaggerr_mcp_disabled": false,
	"autotaggerr_migration_review_artists": false,
	"autotaggerr_migration_review_deletions": false,
	"autotaggerr_migration_review_pinned": false,
	"autotaggerr_migration_review_releases": false,
	"autotaggerr_mirror_cron_schedule": "0 0 3 * * *",
	"autotaggerr_mirror_disabled": false,
	"autotaggerr_name": "Autotaggerr",
	"autotaggerr_port": 8080,
	"autotaggerr_process_concurrency": 4,
	"autotaggerr_process_cron_schedule": "0 0 18 * * 7",
	"autotaggerr_replaced_pin_policy": "review",
	"autotaggerr_tempo_concurrency": 2,
	"autotaggerr_tempo_cron_schedule": "0 0 1 * * *",
	"autotaggerr_loudness_cron_schedule": "0 0 2 * * *",
	"autotaggerr_test_email": "",
	"autotaggerr_version": "v1.0.0",
	"database": {
		"dsn": "config/autotaggerr.db",
		"type": "sqlite"
	},
	"private_key": "",
	"smtp_enabled": true,
	"smtp_from": "",
	"smtp_host": "",
	"smtp_password": "",
	"smtp_port": 0,
	"smtp_tls": "auto",
	"smtp_username": "",
	"timezone": "Europe/Paris"
}
```

Keys are written in alphabetical order, and a key this version does not know is dropped the first
time the file is saved.

</details>

<details>
<summary>Configuration reference</summary>

| Config file entry | Startup flag | Environment variable | Type | Description |
|---|---|---|---|---|
| `timezone` | `-tz` | `TZ` | string | IANA timezone the app runs in. Default `Europe/Paris`. |
| `database.type` | — | — | string | Database driver: `sqlite` (default, pure-Go/CGO-free). `postgres`/`mysql` planned. |
| `database.dsn` | — | — | string | Connection string; for `sqlite` a file path. Default `config/autotaggerr.db`. |
| `private_key` | — | — | string | Auto-generated 64-byte base64 secret (also signs auth tokens). Leave empty; it is created on first start. |
| `autotaggerr_port` | `-port` | `port` | int | HTTP port the service listens on. Default `8080`. |
| `autotaggerr_name` | — | — | string | Display name of the instance. Default `Autotaggerr`. |
| `autotaggerr_external_url` | `-externalurl` | `externalurl` | string | URL others use to reach Autotaggerr. Default empty. |
| `autotaggerr_version` | — | — | string | Build version. Injected at release time; do not set manually. |
| `autotaggerr_environment` | — | — | string | `prod` or `test`. `test` disables Gin release mode and **redirects every outgoing email to `autotaggerr_test_email`**, with no exception. Default `prod`. |
| `autotaggerr_test_email` | — | — | string | Default recipient for the *Send test message* button on **Settings → Email**, and the sole recipient of every message while `autotaggerr_environment` is `test`. Default empty. |
| `autotaggerr_log_level` | — | — | string | Logrus level (`trace`, `debug`, `info`, `warn`, `error`, …). Default `info`. |
| `autotaggerr_process_cron_schedule` | — | — | string | 6-field cron for the recurring processing run. Default `0 0 18 * * 7` (Sundays 18:00). |
| `autotaggerr_process_concurrency` | `-concurrency` | `concurrency` | int | Number of files processed in parallel per library. `1` = serial. Default `4`. |
| `autotaggerr_lidarr_follow_disabled` | — | — | bool | Stop reading Lidarr's history for files it imported, upgraded, deleted or renamed. Default `false` (followed: the artists involved are processed again within minutes). Editable on **Settings → Scanning**. |
| `autotaggerr_lidarr_follow_cron_schedule` | — | — | string | 6-field cron for reading Lidarr's history. Default `0 */10 * * * *` (every ten minutes). A pass that finds nothing records nothing. |
| `autotaggerr_download_poll_cron_schedule` | — | — | string | 6-field cron for checking download clients for finished downloads to offer on Import. Default `0 */2 * * * *` (every two minutes). Nothing is imported without a review. |
| `autotaggerr_mirror_disabled` | — | — | bool | Turn the scheduled MusicBrainz mirror refresh off entirely. Default `false` (the mirror runs). |
| `autotaggerr_mirror_cron_schedule` | — | — | string | 6-field cron for the mirror refresh. Default `0 0 3 * * *` (nightly 03:00). |
| `autotaggerr_artwork_disabled` | — | — | bool | Turn the scheduled artwork fetch, and the automatic one for newly added artists and albums, off entirely. Images are still fetched on demand when a page asks. Default `false` (artwork is fetched ahead). |
| `autotaggerr_artwork_cron_schedule` | — | — | string | 6-field cron for the artwork fetch. Default `0 0 5 * * *` (nightly 05:00). New rows fetch their own artwork as they arrive, so this is a backstop for expiry. |
| `autotaggerr_mcp_disabled` | — | — | bool | Turn off the MCP endpoint (`/api/v1/mcp`) that AI assistants connect to; it then answers 404. Default `false` (on; it takes the same API keys as the REST API, and a read-only key sees only the read tools). Editable on **Settings → AI assistants (MCP)**, applies without a restart. See [docs/mcp.md](docs/mcp.md). |
| `autotaggerr_tempo_cron_schedule` | — | — | string | 6-field cron for tempo analysis. Default `0 0 1 * * *` (nightly 01:00). Which libraries are analysed is set per library (**Libraries → edit**); with none opted in the pass does nothing. Editable on **Settings → Tempo analysis**. |
| `autotaggerr_loudness_cron_schedule` | — | — | string | 6-field cron for loudness (ReplayGain) analysis with rsgain. Default `0 0 2 * * *` (nightly 02:00). Which libraries are analysed is set per library (**Libraries → edit**); with none opted in the pass does nothing. Editable on **Settings → Loudness analysis**. |
| `autotaggerr_tempo_concurrency` | — | — | int | Files analysed in parallel. Default `2`. Analysis is CPU-bound, and a pass pauses while files are being written regardless. Editable on **Settings → Tempo analysis**. |
| `autotaggerr_migration_review_releases` | — | — | bool | Hold merged **releases** for manual approval instead of re-pointing records automatically. Default `false` (apply). |
| `autotaggerr_migration_review_artists` | — | — | bool | Hold merged **artists** for manual approval. Default `false` (apply). |
| `autotaggerr_migration_review_pinned` | — | — | bool | Hold any migration that would rewrite a **manually attached** file's MB ID, whatever its type. Default `false` (apply). |
| `autotaggerr_migration_review_deletions` | — | — | bool | Hold **deleted** entities for manual approval. Applying one marks the affected files unmatched. Default `false` (apply). |
| `autotaggerr_replaced_pin_policy` | — | — | string | What happens when a file attached by hand has its audio replaced: `review` (default, held under **Items → Pin needs review**), `keep` (keep the pin and tag the new audio with it), or `release` (drop the pin; the library's manager identifies the file). Applies to pins already held, from the next run. Editable on **Settings → Manual identifications**. |
| `autotaggerr_event_retention` | — | — | int | How many runs the **Activity** feed keeps. Counted in runs, not rows, so a run's stages are never pruned out from under it. Default `200`; `0` or less means the default. |
| `autotaggerr_event_retention_days` | — | — | int | Keep **Activity** runs by age instead of by count: runs from the last this-many days are kept, older ones are deleted, and `autotaggerr_event_retention` is ignored. Default `0` (keep by count). |
| `autotaggerr_event_detail_retention` | — | — | int | How many per-file (or per-entity) detail rows one Activity event stores. Rows past the cap are counted but not kept, so the event still reports "showing 500 of 3120". Raising it on a busy library grows the database noticeably. Default `500`; `0` or less means the default. |
| `smtp_enabled` | `-disablesmtp` | `disablesmtp` | bool | Enable SMTP mail. The flag/env is inverted: pass `true` to **disable**. Default enabled. Used by the *Send test message* button on **Settings → Email**; nothing else sends mail yet. |
| `smtp_host` | `-smtphost` | `smtphost` | string | SMTP server hostname. Default empty. |
| `smtp_port` | `-smtpport` | `smtpport` | int | SMTP server port. Default `0`. |
| `smtp_tls` | — | — | string | How the connection is encrypted: `auto` (default; implicit TLS on port 465, STARTTLS elsewhere when offered), `none`, `starttls` (refuse to send if not offered), `implicit`. |
| `smtp_username` | `-smtpusername` | `smtpusername` | string | SMTP auth username. Empty means no authentication is attempted. Default empty. |
| `smtp_password` | `-smtppassword` | `smtppassword` | string | SMTP auth password. Default empty. |
| `smtp_from` | `-smtpfrom` | `smtpfrom` | string | Sender address for outgoing mail. Default empty. |
| — | `-file` | `file` | string | Process a single file, then exit instead of running the service. Runtime-only, not stored in config. |
| — | `-fileRoot` | `fileRoot` | string | Library root containing the artist folder for `-file`. Required with `-file`. Runtime-only. |
| — | — | `PUID` | int | **Env only.** UID the container process runs as. Default `1000`. |
| — | — | `PGID` | int | **Env only.** GID the container process runs as. Default `1000`. |

Lidarr is not configured in this file. A Lidarr connection is created under **Managers** in the web
UI, and older `lidarr_*` keys left in a `config.json` are ignored.

</details>

---

## Documentation

Each feature has its own page under [`docs/`](docs/). Start with
[docs/media-manager.md](docs/media-manager.md) for how the parts fit together, and
[docs/development.md](docs/development.md) for building and contributing.

## Roadmap

Planned work and ideas are tracked as [GitHub issues](https://github.com/aunefyren/autotaggerr/issues).

## Contributing

Pull requests, suggestions and issue reports are welcome.
