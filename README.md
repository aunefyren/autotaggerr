# 🎵 Autotaggerr 🎵

[![CI](https://img.shields.io/github/actions/workflow/status/aunefyren/autotaggerr/go.yml?branch=main&style=for-the-badge&label=CI)](https://github.com/aunefyren/autotaggerr/actions/workflows/go.yml)
[![Coverage](https://img.shields.io/endpoint?url=https://gist.githubusercontent.com/aunefyren/111899f93369f8f73ab6a4296d23da9a/raw/autotaggerr-coverage.json&style=for-the-badge)](https://github.com/aunefyren/autotaggerr/actions/workflows/go.yml)

**Autotaggerr** is an automated music tagging utility that enriches your Lidarr managed audio library with detailed metadata from [MusicBrainz](https://musicbrainz.org/). It identifies tracks based on their MusicBrainz Release ID (used by tools like [Lidarr](https://lidarr.audio/)), or by talking to Lidarr through API. it then fills in missing metadata, including: track artists, release date, genre, track numbers, and more. It can automatically refresh the metadata in Plex afterward.

> Built for automation of large libraries!

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
  - MP3 via [`ffmpeg`](https://ffmpeg.org/)

- 🖼️ **Album & Artist Artwork**  
  Covers come from the [Cover Art Archive](https://coverartarchive.org) with no setup at all. For
  artist portraits and backdrops, add a **fanart.tv** data source with your own free API key — MusicBrainz
  has no artist images, so that is the only source for them. Without a key, artists simply show
  monogram tiles. All artwork is proxied and cached by Autotaggerr, so nothing is hot-linked and your
  key never reaches the browser.

- 🧠 **Rate-Limited & Cached API Calls**  
  Avoid API abuse and repeated lookups with built-in caching and configurable request throttling.

- 🐳 **Containerized (Docker-ready)**  
  Small, clean and minimal Docker image with `ffmpeg` and `metaflac` included.

---

## 🛠️ How It Works

1. Scans your music library (recursively).
2. Extracts the MusicBrainz Release ID from FLAC/MP3 files. Can fall back to Lidarr API.
3. Queries MusicBrainz to retrieve release data.
4. Writes metadata tags to files:
   - FLAC → via `metaflac`
   - MP3 → via `ffmpeg`
5. Optionally logs and caches results to avoid re-fetching metadata.
6. Optionally informs Plex to refresh the metadata

---

## 🛠️ Caveats

1. Plex does not support multi-artist albums. So even if the metadata should have multiple artist as the album artist, Autotaggerr just tags the primary one
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

Make sure these are installed **if you're not using Docker**:

### 🔧 [FLAC / Metaflac](https://xiph.org/flac/download.html)

Used to read/write Vorbis comments in `.flac` files.

- **Windows (choco)**  
  `choco install flac`
- **Ubuntu/Debian**  
  `sudo apt install flac`

---

### 🎞 [FFmpeg](https://ffmpeg.org/)

Used for updating metadata in `.mp3` files.

- **Windows (choco)**  
  `choco install ffmpeg`
- **Ubuntu/Debian**  
  `sudo apt install ffmpeg`

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
	"lidarr_base_url": "https://lidarr.mycooldomain.com",
	"lidarr_api_key": "XXX",
	"lidarr_header_cookie": "",
	"plex_base_url": "https://plex.mycooldomain.com",
	"plex_token": "XXX"
}
```

### 🔧 Configuration reference

Every setting can be defined in `config.json`. A subset can also be overridden at runtime with a startup flag or an environment variable (the container `entrypoint.sh` maps env vars onto the flags). Precedence is: **startup flag → environment variable → config file value**. A flag/env only overrides the config when it is explicitly provided.

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
| `autotaggerr_environment` | — | — | string | `prod` or `test`. `test` disables Gin release mode. Default `prod`. |
| `autotaggerr_test_email` | — | — | string | Address used for SMTP test mail. Default empty. |
| `autotaggerr_log_level` | — | — | string | Logrus level (`trace`, `debug`, `info`, `warn`, `error`, …). Default `info`. |
| `autotaggerr_libraries` | — | — | string[] | Absolute paths of music libraries to scan recursively. Default `[]`. |
| `autotaggerr_process_on_start_up` | — | — | bool | Run a full library scan immediately on startup. Default `false`. |
| `autotaggerr_process_cron_schedule` | — | — | string | 6-field cron for the recurring scan. Default `0 0 18 * * 7` (Sundays 18:00). |
| `autotaggerr_process_concurrency` | `-concurrency` | `concurrency` | int | Number of files processed in parallel per library scan. `1` = serial. Default `4`. |
| `autotaggerr_use_current_artist_name` | — | — | bool | Prefer the artist's current name over the credited name. Default `true`. |
| `autotaggerr_ignore_redundant_contributing_artists` | — | — | bool | Drop contributing artists already covered by the album artist. Default `true`. |
| `autotaggerr_use_custom_artist_delimiter` | — | — | bool | Join multiple artists with a custom delimiter. Default `true`. |
| `autotaggerr_custom_artist_delimiter` | — | — | string | Delimiter used when joining artists. Default `" & "`. |
| `autotaggerr_custom_artist_delimiter_commas` | — | — | bool | Use commas between artists, with the custom delimiter before the last. Default `true`. |
| `autotaggerr_remove_values` | — | — | bool | Remove existing tag values not present in the new metadata. Default `false`. |
| `smtp_enabled` | `-disablesmtp` | `disablesmtp` | bool | Enable SMTP mail. The flag/env is inverted: pass `true` to **disable**. Default enabled. |
| `smtp_host` | `-smtphost` | `smtphost` | string | SMTP server hostname. Default empty. |
| `smtp_port` | `-smtpport` | `smtpport` | int | SMTP server port. Default `0`. |
| `smtp_username` | `-smtpusername` | `smtpusername` | string | SMTP auth username. Default empty. |
| `smtp_password` | `-smtppassword` | `smtppassword` | string | SMTP auth password. Default empty. |
| `smtp_from` | `-smtpfrom` | `smtpfrom` | string | Sender address for outgoing mail. Default empty. |
| `lidarr_base_url` | — | — | string | Base URL of the Lidarr instance (fallback metadata source). Default empty. |
| `lidarr_api_key` | — | — | string | Lidarr API key. Default empty. |
| `lidarr_header_cookie` | — | — | string | Optional cookie header sent with Lidarr requests (e.g. for a reverse proxy). Default empty. |
| `plex_base_url` | — | — | string | Base URL of the Plex instance to refresh. Default empty. |
| `plex_token` | — | — | string | Plex auth token. Default empty. |
| — | `-file` | `file` | string | Process a single file, then exit instead of running the service. Runtime-only, not stored in config. |
| — | `-fileRoot` | `fileRoot` | string | Library root containing the artist folder for `-file`. Required with `-file`. Runtime-only. |
| — | — | `PUID` | int | **Env only.** UID the container process runs as. Default `1000`. |
| — | — | `PGID` | int | **Env only.** GID the container process runs as. Default `1000`. |

---

## 🧠 Roadmap Ideas

Support for other formats (OGG, M4A, etc)

More metadata

---

## 👋 Contributing

Pull requests, suggestions, and issue reports are welcome!
Feel free to fork.

---