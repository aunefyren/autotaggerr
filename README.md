# Autotaggerr
Scans music libraries and expands Musicbrainz metadata

## Dependencies
### Install metaflac
Website: https://xiph.org/flac/download.html

Choco: choco install flac

Ubuntu: sudo apt install flac

### Install FFMPEG
Website: https://ffmpeg.org/

Choco: choco install ffmpeg

Ubuntu: sudo apt install ffmpeg

## Docker compose example
```
services:
  autotaggerr:
    container_name: autotaggerr-app
    image: ghcr.io/aunefyren/autotaggerr:beta
    restart: unless-stopped
    volumes:
      - ./data/:/app/config/:rw
      - /media/library/music/:/music/:rw
    environment:
      # These will overwrite the config.json
      PORT: 8080
      TZ: Europe/Oslo
      PUID: 1000
      PGID: 1000
```