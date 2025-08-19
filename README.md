# 🎵 Autotaggerr 🎵

**Autotaggerr** is an automated music tagging utility that enriches your Lidarr managed audio library with detailed metadata from [MusicBrainz](https://musicbrainz.org/). It identifies tracks based on their MusicBrainz Release ID (used by tools like [Lidarr](https://lidarr.audio/)), or by talking to Lidarr through API, and fills in missing metadata — including track artists, release date, genre, track numbers, and more. It can automatically refresh the metadata in Plex afterward.

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
	"lidarr_base_url": "https://lidarr.mycooldomain.com",
	"lidarr_api_key": "XXX",
	"lidarr_header_cookie": "",
	"plex_base_url": "https://plex.mycooldomain.com",
	"plex_token": "XXX"
}
```

---

## 🧠 Roadmap Ideas

Support for other formats (OGG, M4A, etc)

More metadata

---

## 👋 Contributing

Pull requests, suggestions, and issue reports are welcome!
Feel free to fork.

---