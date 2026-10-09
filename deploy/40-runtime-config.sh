#!/bin/sh
set -eu

case "${APP_BACKGROUND_COLOR:-green}" in
  blue) background_color=blue ;;
  green|"") background_color=green ;;
  *) background_color=green ;;
esac

printf '{"backgroundColor":"%s"}\n' "$background_color" > /tmp/ottawa-runtime-config.json
