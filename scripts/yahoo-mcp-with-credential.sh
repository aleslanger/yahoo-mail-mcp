#!/usr/bin/env bash
set -euo pipefail

# Compatibility launcher: yahoo-mcp itself reads and validates the private
# credential file when the environment variable is absent.
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
exec "$script_dir/../yahoo-mcp" "$@"
