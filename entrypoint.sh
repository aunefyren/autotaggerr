#!/bin/sh

# Start with the binary
set -- /app/autotaggerr

# Map environment variables onto CLI flags when set
[ -n "$port" ] && set -- "$@" --port "$port"
[ -n "$externalurl" ] && set -- "$@" --externalurl "$externalurl"
[ -n "$TZ" ] && set -- "$@" --tz "$TZ"
[ -n "$concurrency" ] && set -- "$@" --concurrency "$concurrency"
[ -n "$file" ] && set -- "$@" --file "$file"
[ -n "$fileRoot" ] && set -- "$@" --fileRoot "$fileRoot"

# SMTP settings
[ -n "$disablesmtp" ] && set -- "$@" --disablesmtp "$disablesmtp"
[ -n "$smtphost" ] && set -- "$@" --smtphost "$smtphost"
[ -n "$smtpport" ] && set -- "$@" --smtpport "$smtpport"
[ -n "$smtpusername" ] && set -- "$@" --smtpusername "$smtpusername"
[ -n "$smtppassword" ] && set -- "$@" --smtppassword "$smtppassword"
[ -n "$smtpfrom" ] && set -- "$@" --smtpfrom "$smtpfrom"

# When started as root, honor PUID/PGID: fix ownership of the writable config
# directory, then drop privileges to that uid/gid. If the container is already
# running as a non-root user (e.g. `docker run --user`), just exec directly.
if [ "$(id -u)" = "0" ]; then
  PUID="${PUID:-1000}"
  PGID="${PGID:-1000}"
  chown -R "${PUID}:${PGID}" /app/config 2>/dev/null || true
  exec su-exec "${PUID}:${PGID}" "$@"
fi

# Execute safely
exec "$@"
