#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(dirname "$SCRIPT_DIR")"
GO_VERSION="1.25.11"
GO_IMAGE="golang:${GO_VERSION}-bookworm"

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

for cmd in git gpg sha256sum; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo "Missing required command: $cmd" >&2
        exit 1
    fi
done

USE_DOCKER=false
if command -v docker >/dev/null 2>&1; then
    USE_DOCKER=true
elif command -v go >/dev/null 2>&1; then
    GO_ACTUAL="$(go version | awk '{print $3}')"
    if [[ "$GO_ACTUAL" != "go${GO_VERSION}" ]]; then
        echo "Go version mismatch: expected go${GO_VERSION}, got ${GO_ACTUAL}" >&2
        echo "Install Go ${GO_VERSION} or install Docker to build inside ${GO_IMAGE}." >&2
        exit 1
    fi
else
    echo "Missing required command: docker or go" >&2
    exit 1
fi

SOURCE_DATE_EPOCH="$(git -C "$REPO_ROOT" log -1 --pretty=%ct)"
BUILD_DIR="$REPO_ROOT/tmp/release-build/$VERSION"
SIGNED_DIR="$REPO_ROOT/signatures/$VERSION"

rm -rf "$BUILD_DIR"
mkdir -p "$BUILD_DIR"
chmod 0777 "$BUILD_DIR"
mkdir -p "$SIGNED_DIR"

# Must stay in sync with the build matrix in .github/workflows/release.yaml.
# Fields: GOOS GOARCH GOARM OUTPUT ("-" for GOARM when not applicable).
declare -a TARGETS=(
    "linux amd64 - neutrinod-linux-amd64"
    "linux 386 - neutrinod-linux-386"
    "linux arm64 - neutrinod-linux-arm64"
    "linux arm 7 neutrinod-linux-armv7"
    "linux arm 6 neutrinod-linux-armv6"
    "darwin amd64 - neutrinod-darwin-amd64"
    "darwin arm64 - neutrinod-darwin-arm64"
    "windows amd64 - neutrinod-windows-amd64.exe"
    "windows arm64 - neutrinod-windows-arm64.exe"
)

for target in "${TARGETS[@]}"; do
    read -r GOOS GOARCH GOARM OUTPUT <<<"$target"
    if [[ "$GOARM" == "-" ]]; then
        GOARM=""
    fi
    echo "Building $OUTPUT"

    if [[ "$USE_DOCKER" == true ]]; then
        docker run --rm \
            --user "$(id -u):$(id -g)" \
            -e GOOS="$GOOS" \
            -e GOARCH="$GOARCH" \
            -e GOARM="$GOARM" \
            -e CGO_ENABLED=0 \
            -e SOURCE_DATE_EPOCH="$SOURCE_DATE_EPOCH" \
            -e GOCACHE=/tmp/go-build-cache \
            -e GOPATH=/tmp/go \
            -e GOMODCACHE=/tmp/go/pkg/mod \
            -v "$REPO_ROOT:/workspace" \
            -w /workspace/neutrino_server \
            "$GO_IMAGE" \
            go build \
                -trimpath \
                -buildvcs=false \
                -ldflags="-buildid= -s -w -X main.version=${VERSION}" \
                -o "/workspace/tmp/release-build/${VERSION}/${OUTPUT}" \
                ./cmd/neutrinod
    else
        (
            cd "$REPO_ROOT/neutrino_server"
            GOOS="$GOOS" \
            GOARCH="$GOARCH" \
            GOARM="$GOARM" \
            CGO_ENABLED=0 \
            SOURCE_DATE_EPOCH="$SOURCE_DATE_EPOCH" \
            go build \
                -trimpath \
                -buildvcs=false \
                -ldflags="-buildid= -s -w -X main.version=${VERSION}" \
                -o "$BUILD_DIR/$OUTPUT" \
                ./cmd/neutrinod
        )
    fi
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
