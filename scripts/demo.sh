#!/bin/sh
set -eu

if command -v onthego >/dev/null 2>&1; then
  ONTHEGO_BIN=onthego
else
  ONTHEGO_BIN="$(pwd)/dist/onthego"
  if [ ! -x "$ONTHEGO_BIN" ]; then
    mkdir -p dist
    go build -o "$ONTHEGO_BIN" ./cmd/onthego
  fi
fi

run() {
  printf '\n$ %s\n' "$*"
  "$@"
}

run "$ONTHEGO_BIN" --help
run "$ONTHEGO_BIN" init
run "$ONTHEGO_BIN" login
run "$ONTHEGO_BIN" pass
run "$ONTHEGO_BIN" status
run "$ONTHEGO_BIN" pull
