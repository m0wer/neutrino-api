#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(dirname "$SCRIPT_DIR")"

usage() {
    cat <<EOF
Usage: $(basename "$0") <version> [--key <fingerprint-or-email>]

Build release binaries reproducibly, generate SHA256SUMS, and sign the digest.

Examples:
  $(basename "$0") v0.8.0
  $(basename "$0") v0.8.0 --key 1C53A412D11EF3051704419C44912E1E03005B31
EOF
}

if [[ $# -lt 1 ]]; then
    usage
    exit 1
fi

VERSION="$1"
shift

GPG_KEY=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --key)
            GPG_KEY="$2"
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "Unknown argument: $1" >&2
            usage
            exit 1
            ;;
    esac
done

for cmd in git go gpg sha256sum; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo "Missing required command: $cmd" >&2
        exit 1
    fi
done

SOURCE_DATE_EPOCH="$(git -C "$REPO_ROOT" log -1 --pretty=%ct)"
BUILD_DIR="$REPO_ROOT/tmp/release-build/$VERSION"
SIGNED_DIR="$REPO_ROOT/signatures/$VERSION"

rm -rf "$BUILD_DIR"
mkdir -p "$BUILD_DIR"
mkdir -p "$SIGNED_DIR"

declare -a TARGETS=(
    "linux amd64 neutrinod-linux-amd64"
    "linux arm64 neutrinod-linux-arm64"
    "darwin amd64 neutrinod-darwin-amd64"
    "darwin arm64 neutrinod-darwin-arm64"
    "windows amd64 neutrinod-windows-amd64.exe"
)

for target in "${TARGETS[@]}"; do
    read -r GOOS GOARCH OUTPUT <<<"$target"
    echo "Building $OUTPUT"

    (
        cd "$REPO_ROOT/neutrino_server"
        GOOS="$GOOS" \
        GOARCH="$GOARCH" \
        CGO_ENABLED=0 \
        SOURCE_DATE_EPOCH="$SOURCE_DATE_EPOCH" \
        go build \
            -trimpath \
            -buildvcs=false \
            -ldflags="-buildid= -s -w -X main.version=${VERSION}" \
            -o "$BUILD_DIR/$OUTPUT" \
            ./cmd/neutrinod
    )
done

(
    cd "$BUILD_DIR"
    sha256sum neutrinod-* | sort > SHA256SUMS
)

cp "$BUILD_DIR/SHA256SUMS" "$SIGNED_DIR/SHA256SUMS"

SIGN_ARGS=(--armor --detach-sign)
if [[ -n "$GPG_KEY" ]]; then
    SIGN_ARGS+=(--local-user "$GPG_KEY")
fi

gpg "${SIGN_ARGS[@]}" \
    --output "$SIGNED_DIR/SHA256SUMS.asc" \
    "$SIGNED_DIR/SHA256SUMS"

gpg --verify "$SIGNED_DIR/SHA256SUMS.asc" "$SIGNED_DIR/SHA256SUMS"

echo
echo "Release digest and signature created:"
echo "  $SIGNED_DIR/SHA256SUMS"
echo "  $SIGNED_DIR/SHA256SUMS.asc"
echo
echo "Temporary build outputs: $BUILD_DIR"
