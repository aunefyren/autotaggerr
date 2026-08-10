# 🎵 Autotaggerr 🎵

[![CI](https://img.shields.io/github/actions/workflow/status/aunefyren/autotaggerr/go.yml?branch=main&style=for-the-badge&label=CI)](https://github.com/aunefyren/autotaggerr/actions/workflows/go.yml)
[![Coverage](https://img.shields.io/endpoint?url=https://gist.githubusercontent.com/aunefyren/111899f93369f8f73ab6a4296d23da9a/raw/autotaggerr-coverage.json&style=for-the-badge)](https://github.com/aunefyren/autotaggerr/actions/workflows/go.yml)

**Autotaggerr** is an automated music tagging utility that enriches your Lidarr managed audio library with detailed metadata from [MusicBrainz](https://musicbrainz.org/). It identifies tracks based on their MusicBrainz Release ID (used by tools like [Lidarr](https://lidarr.audio/)), or by talking to Lidarr through API. it then fills in missing metadata, including: track artists, release date, genre, track numbers, and more. It can automatically refresh the metadata in Plex afterward.

> [!WARNING]
> There is currently no official release of this software, it is in beta and has not been advertised. Get in contact with me if you are interested in it.

---

## Context

This tool is for a specific niche, but feel free to use it if it fits your use case. I use PlexAmp as a music player, and Lidarr as a music catalog tool. Lidarr its job well for the most part, except for the metadata. They do not tag all the data available from Musicbrainz, and based on my dialogue with them, they have no intention of fixing this. Solution: I'll tag the files myself.

PlexAmp/Plex is a lot smoother with good metadata attached. Lidarr already selected a Musicbrainz release when it imported the music, I just need to apply all the data from that source. Here is the desired result within PlexAmp, notice the featuring artists:

![Demo video](https://raw.githubusercontent.com/aunefyren/autotaggerr/refs/heads/main/.github/assets/demo.gif)

There are other solutions that try to fix this, like a Beets plugin that can run on top of Lidarr. I found this solution very confusing to set up, and it seemed to rely on auto-matching track titles, which I did not like. I already know the Musicbrainz release chosen.

---

## 🚀 Features

- 📂 **Recursive Library Scanning**  
  Traverse your music directories and find FLAC and MP3 files automatically.

- 🧠 **MusicBrainz Integration**  
  Uses the MusicBrainz API to fetch detailed metadata using release IDs already embedded in your files (via Lidarr, etc).

- 🏷️ **FLAC + MP3 Tagging**  
  Updates:
  - FLAC via [`metaflac`](https://xiph.org/flac/)
  - MP3 natively, no external tool required

- 🖼️ **Album & Artist Artwork**  
  Covers come from the [Cover Art Archive](https://coverartarchive.org) with no setup at all. For
  artist portraits and backdrops, add a **fanart.tv** data source with your own free API key — MusicBrainz
  has no artist images, so that is the only source for them. Without a key, artists simply show
  monogram tiles. All artwork is proxied and cached by Autotaggerr, so nothing is hot-linked and your
  key never reaches the browser.

- 🧠 **Rate-Limited & Cached API Calls**  
  Avoid API abuse and repeated lookups with built-in caching and configurable request throttling.

- 🐳 **Containerized (Docker-ready)**  
  Small, clean and minimal Docker image with `metaflac` included.

---

## 🛠️ How It Works

1. Scans your music library (recursively).
2. Extracts the MusicBrainz Release ID from FLAC/MP3 files. Can fall back to Lidarr API.
3. Queries MusicBrainz to retrieve release data.
4. Writes metadata tags to files:
   - FLAC → via `metaflac`
   - MP3 → natively
5. Optionally logs and caches results to avoid re-fetching metadata.
6. Optionally informs Plex to refresh the metadata

---

## 🛠️ Caveats

1. Plex does not support multi-artist albums, and renders a joined string as one artist literally named `A; B`. So `ALBUMARTIST` gets the primary artist only; the full credit is written alongside it as `ALBUMARTISTS`, which players that understand it (Navidrome, Picard) read instead. The same applies to genres on MP3: Plex reads tags through ffmpeg, which sees only the first value of a proper multi-value tag, so MP3s join them with `; ` unless the tagger profile's `mp3_multi_value_tags` is on. FLAC always uses the multi-value form — ffmpeg joins those back together on its own, so nothing is lost either way.
2. Autotaggerr can at times utilize the path of the file to determine what metadata is correct. Therefore, you must use this structure `/music-library-root/[ARTIST]/[ALBUM] ([YEAR])/[OPTIONAL MEDIA FOLDER]/[TRACKS])`
3. Autotaggerr will first look for the Musicbrainz release/track ID within the file tags. If none are found, a Lidarr client must be configured for fallback. This is necessary for MP3 files as Lidarr does not tag these IDs on MP3s
4.  Lidarr tends to overwrite tags for some reason. Go to Lidarr -> Settings -> Metadata:
    - Set `Tag Audio Files with Metadata` to `For new downloads only`
    - Set `Scrub Existing Tags` to unchecked
5. Plex must be set up to respect local metadata:
    - Library -> Manage library -> Edit -> Advanced -> Check `Prefer local metadata`
6. Jellyfin identifies artists by name, not by MusicBrainz ID (which Autotaggerr does tag). If an online provider (e.g. TheAudioDB) spells an artist differently than MusicBrainz — such as a straight `'` vs a curly `’` apostrophe — Jellyfin can show duplicate artists and even bake both spellings into `album.nfo`. It may therefore be wise to disable the `Nfo` metadata reader (and/or TheAudioDB) for your music library so Jellyfin trusts the embedded tags Autotaggerr writes:
    - Dashboard -> Libraries -> your music library -> uncheck `Nfo` under the metadata readers/downloaders

---

## 📦 Dependencies

One binary, and only **if you're not using Docker**:

### 🔧 [FLAC / Metaflac](https://xiph.org/flac/download.html)

Used to read/write Vorbis comments in `.flac` files.

- **Windows (choco)**  
  `choco install flac`
- **Ubuntu/Debian**  
  `sudo apt install flac`

MP3 tagging needs nothing installed — ID3 frames are read and written in-process.
[`fpcalc`](https://acoustid.org/chromaprint) is optional, for AcoustID fingerprinting.

---

## 🐳 Docker Compose Example

Autotaggerr runs well as a background service. Here's how to set it up with Docker Compose:

```yaml
services:
  autotaggerr:
    container_name: autotaggerr-app
    image: ghcr.io/aunefyren/autotaggerr:beta
    restart: unless-stopped
    volumes:
      - ./data/:/app/config/:rw               # Config and cache
      - /media/library/music/:/music/:rw      # Your music library
    environment:
      # These override config.json settings
      PORT: 8080
      TZ: Europe/Paris
      PUID: 1000
      PGID: 1000
```

---

## 🐳 Configuring Autotaggerr
Edit the config.json, found within the config directory. If it isn't there, just start the application first. Example:

```json
{
	"timezone": "Europe/Paris",
	"database": {
		"type": "sqlite",
		"dsn": "config/autotaggerr.db"
	},
	"private_key": "",
	"autotaggerr_port": 8080,
	"autotaggerr_name": "Autotaggerr",
	"autotaggerr_external_url": "",
	"autotaggerr_version": "v1.0.0",
	"autotaggerr_environment": "prod",
	"autotaggerr_test_email": "",
	"autotaggerr_log_level": "info",
	"autotaggerr_libraries": [
		"/media/library/music"
	],
	"autotaggerr_process_on_start_up": true,
	"autotaggerr_process_cron_schedule": "0 0 18 * * 7",
	"autotaggerr_process_concurrency": 4,
	"autotaggerr_mirror_cron_schedule": "0 0 3 * * *",
	"lidarr_base_url": "https://lidarr.mycooldomain.com",
	"lidarr_api_key": "XXX",
	"lidarr_header_cookie": "",
	"plex_base_url": "https://plex.mycooldomain.com",
	"plex_token": "XXX"
}
```

### 🔧 Configuration reference

Every setting can be defined in `config.json`. A subset can also be overridden at runtime with a startup flag or an environment variable (the container `entrypoint.sh` maps env vars onto the flags). Precedence is: **startup flag → environment variable → config file value**. A flag/env only overrides the config when it is explicitly provided.

**You do not have to edit this file by hand.** Signed in as an admin, the **Settings** page edits the
same keys from the web UI: schedules, log level, processing concurrency and the mirror switch take effect
immediately, and the rest are saved and picked up at the next start (the page says which is which).
The keys marked *managed elsewhere* below are the exception — they seeded the database on first start
and are edited on the Managers, Tagger profiles and Libraries pages now. See
[docs/settings.md](docs/settings.md).

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
| `autotaggerr_libraries` | — | — | string[] | Absolute paths of music libraries to process recursively. Default `[]`. |
| `autotaggerr_process_on_start_up` | — | — | bool | Run a full processing pass over every library immediately on startup. Default `false`. |
| `autotaggerr_process_cron_schedule` | — | — | string | 6-field cron for the recurring processing run. Default `0 0 18 * * 7` (Sundays 18:00). |
| `autotaggerr_process_concurrency` | `-concurrency` | `concurrency` | int | Number of files processed in parallel per library. `1` = serial. Default `4`. |
| `autotaggerr_use_current_artist_name` | — | — | bool | Prefer the artist's current name over the credited name. Default `true`. |
| `autotaggerr_ignore_redundant_contributing_artists` | — | — | bool | Drop contributing artists already covered by the album artist. Default `true`. |
| `autotaggerr_use_custom_artist_delimiter` | — | — | bool | Join multiple artists with a custom delimiter. Default `true`. |
| `autotaggerr_custom_artist_delimiter` | — | — | string | Delimiter used when joining artists. Default `" & "`. |
| `autotaggerr_custom_artist_delimiter_commas` | — | — | bool | Use commas between artists, with the custom delimiter before the last. Default `true`. |
| `autotaggerr_remove_values` | — | — | bool | Remove existing tag values not present in the new metadata. Default `false`. |
| `autotaggerr_max_genres` | — | — | int | How many of a release group's genres are written to `GENRE`, most-voted first. Default `5`. |
| `autotaggerr_mp3_multi_value_tags` | — | — | bool | Write ID3v2.4's own multi-value form in MP3s instead of joining values with `; `. Default `false` — Plex reads tags through ffmpeg, which sees only the first value. FLAC always uses the multi-value form. |
| `autotaggerr_mirror_disabled` | — | — | bool | Turn the scheduled MusicBrainz mirror refresh off entirely. Default `false` (the mirror runs). |
| `autotaggerr_mirror_cron_schedule` | — | — | string | 6-field cron for the mirror refresh. Default `0 0 3 * * *` (nightly 03:00). |
| `autotaggerr_mirror_on_start_up` | — | — | bool | Also run a mirror pass on startup. Default `false` — a first pass over a large collection is hours of rate-limited fetching. |
| `autotaggerr_migration_review_releases` | — | — | bool | Hold merged **releases** for manual approval instead of re-pointing records automatically. Default `false` (apply). |
| `autotaggerr_migration_review_artists` | — | — | bool | Hold merged **artists** for manual approval. Default `false` (apply). |
| `autotaggerr_migration_review_pinned` | — | — | bool | Hold any migration that would rewrite a **manually attached** file's MB ID, whatever its type. Default `false` (apply). |
| `autotaggerr_migration_review_deletions` | — | — | bool | Hold **deleted** entities for manual approval. Applying one marks the affected files unmatched. Default `false` (apply). |
| `smtp_enabled` | `-disablesmtp` | `disablesmtp` | bool | Enable SMTP mail. The flag/env is inverted: pass `true` to **disable**. Default enabled. Used by the *Send test message* button on **Settings → Email**; nothing else sends mail yet. |
| `smtp_host` | `-smtphost` | `smtphost` | string | SMTP server hostname. Default empty. |
| `smtp_port` | `-smtpport` | `smtpport` | int | SMTP server port. Default `0`. |
| `smtp_tls` | — | — | string | How the connection is encrypted: `auto` (default — implicit TLS on port 465, STARTTLS elsewhere when offered), `none`, `starttls` (refuse to send if not offered), `implicit`. |
| `smtp_username` | `-smtpusername` | `smtpusername` | string | SMTP auth username. Empty means no authentication is attempted. Default empty. |
| `smtp_password` | `-smtppassword` | `smtppassword` | string | SMTP auth password. Default empty. |
| `smtp_from` | `-smtpfrom` | `smtpfrom` | string | Sender address for outgoing mail. Default empty. |
| `lidarr_base_url` | — | — | string | **Seed only** (see below). Base URL of the Lidarr instance. Default empty. |
| `lidarr_api_key` | — | — | string | **Seed only.** Lidarr API key. Default empty. |
| `lidarr_header_cookie` | — | — | string | **Seed only.** Optional `name=value` cookie sent with Lidarr requests, for an authentication proxy such as Authelia. Default empty. |
| `plex_base_url` | — | — | string | Base URL of the Plex instance to refresh. Default empty. |
| `plex_token` | — | — | string | Plex auth token. Default empty. |
| — | `-file` | `file` | string | Process a single file, then exit instead of running the service. Runtime-only, not stored in config. |
| — | `-fileRoot` | `fileRoot` | string | Library root containing the artist folder for `-file`. Required with `-file`. Runtime-only. |
| — | — | `PUID` | int | **Env only.** UID the container process runs as. Default `1000`. |
| — | — | `PGID` | int | **Env only.** GID the container process runs as. Default `1000`. |

> **Seed only:** the three `lidarr_*` keys are copied into the Lidarr **manager** record on first
> run and never read from `config.json` again. Everything that talks to Lidarr — scans and the
> health check alike — uses the manager record, so change these under **Managers** in the web UI.
> Editing them in `config.json` after the first run has no effect — and if you do, the log says so
> on the next startup, naming the keys that no longer match the manager. The *Test* button there
> probes the connection with exactly the credentials a scan would use; an auth-proxy cookie expires
> on its own schedule, and that button is how you find out it has.

---

## 🧠 Roadmap Ideas

Support for other formats (OGG, M4A, etc)

More metadata

---

## 👋 Contributing

Pull requests, suggestions, and issue reports are welcome!
Feel free to fork.

---