# 🎵 Autotaggerr 🎵

**Autotaggerr** is an automated music tagging utility that enriches your audio library with detailed metadata from [MusicBrainz](https://musicbrainz.org/). It identifies tracks based on their MusicBrainz Release ID (used by tools like [Lidarr](https://lidarr.audio/)) and fills in missing metadata — including album artist, release date, genre, track numbers, and more.

> Built for automation of large libraries!

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
  Clean and minimal Docker image with `ffmpeg` and `metaflac` included.

---

## 🛠️ How It Works

1. Scans your music library (recursively).
2. Extracts the MusicBrainz Release ID from FLAC/MP3 files.
3. Queries MusicBrainz to retrieve release data.
4. Writes metadata tags to files:
   - FLAC → via `metaflac`
   - MP3 → via `ffmpeg`
5. Optionally logs and caches results to avoid re-fetching metadata.

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

## 🧠 Roadmap Ideas

Web UI (status and manual tagging)

More advanced tag merging options

Support for other formats (OGG, M4A, etc)

    Integration with Lidarr API

## 👋 Contributing

Pull requests, suggestions, and issue reports are welcome!
Feel free to fork.

---