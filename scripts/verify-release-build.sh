#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(dirname "$SCRIPT_DIR")"
GO_VERSION="1.25.0"
GO_IMAGE="golang:${GO_VERSION}-bookworm"

usage() {
    cat <<EOF
Usage: $(basename "$0") <version>

Rebuild release binaries reproducibly and verify they match the signed digest.

Example:
  $(basename "$0") v1.0.0
EOF
}

if [[ $# -ne 1 ]]; then
    usage
    exit 1
fi

VERSION="$1"
SIGNED_DIR="$REPO_ROOT/signatures/$VERSION"
EXPECTED_SUMS="$SIGNED_DIR/SHA256SUMS"
SIGNATURE_FILE="$SIGNED_DIR/SHA256SUMS.asc"

for cmd in git gpg sha256sum diff; do
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

if [[ ! -f "$EXPECTED_SUMS" ]]; then
    echo "Missing digest file: $EXPECTED_SUMS" >&2
    exit 1
fi

if [[ ! -f "$SIGNATURE_FILE" ]]; then
    echo "Missing signature file: $SIGNATURE_FILE" >&2
    exit 1
fi

if compgen -G "$REPO_ROOT/signatures/pubkeys/*.asc" >/dev/null; then
    for pubkey in "$REPO_ROOT"/signatures/pubkeys/*.asc; do
        gpg --import "$pubkey" >/dev/null 2>&1 || true
    done
fi

echo "Verifying signature on $EXPECTED_SUMS"
gpg --verify "$SIGNATURE_FILE" "$EXPECTED_SUMS"

SOURCE_DATE_EPOCH="$(git -C "$REPO_ROOT" log -1 --pretty=%ct)"
mkdir -p "$REPO_ROOT/tmp"
BUILD_DIR="$(mktemp -d "$REPO_ROOT/tmp/repro-build.${VERSION}.XXXXXX")"
chmod 0777 "$BUILD_DIR"
trap 'rm -rf "$BUILD_DIR"' EXIT

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

    if [[ "$USE_DOCKER" == true ]]; then
        docker run --rm \
            --user "$(id -u):$(id -g)" \
            -e GOOS="$GOOS" \
            -e GOARCH="$GOARCH" \
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
                -o "/workspace/${BUILD_DIR#"$REPO_ROOT/"}/${OUTPUT}" \
                ./cmd/neutrinod
    else
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
    fi
done

(
    cd "$BUILD_DIR"
    sha256sum neutrinod-* | sort > SHA256SUMS
)

echo "Comparing rebuilt checksums with signed digest"
diff -u "$EXPECTED_SUMS" "$BUILD_DIR/SHA256SUMS"

echo
echo "Verification successful: locally rebuilt binaries match signed SHA256SUMS for $VERSION"
