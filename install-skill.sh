#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
VERSION="${FEATURE_IMPLEMENT_VERSION:-$(git -C "$SCRIPT_DIR" describe --tags --exact-match 2>/dev/null || true)}"
VERSION="${VERSION:-dev}"

export FEATURE_IMPLEMENT_REPO_ROOT="$SCRIPT_DIR"
exec go run -ldflags "-X main.Version=$VERSION" "$SCRIPT_DIR/cmd/feature" install-skills "$@"
