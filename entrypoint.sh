#!/bin/sh

# Initialize the command with the binary
CMD="/app/autotaggerr"

# Add the --port flag if the PORT environment variable is set
if [ -n "$port" ]; then
  CMD="$CMD --port $port"
fi

# Add the --timezone flag if the TIMEZONE environment variable is set
if [ -n "$timezone" ]; then
  CMD="$CMD --timezone $timezone"
fi

# add the --file
if [ -n "$file" ]; then
  CMD="$CMD --file $file"
fi

# Execute the final command
exec $CMD