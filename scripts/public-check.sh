#!/bin/sh
# Lists tracked lines that name private infrastructure or documents
# (scripts/publiccheck); fails if there are any.
set -eu
cd "$(dirname "$0")/.."
CGO_ENABLED=0 go run ./scripts/publiccheck
