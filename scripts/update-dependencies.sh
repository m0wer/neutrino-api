#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(dirname "$SCRIPT_DIR")"

# Update these reviewed pins together, then run this script before each release.
GO_VERSION="1.27.0"
ALPINE_VERSION="3.23.5"
NEUTRINO_VERSION="v0.18.0"
NEUTRINO_FORK_VERSION="v0.0.0-20260731081950-cae7469a3a18"
BTCD_VERSION="v0.26.2"
STATICCHECK_VERSION="v0.8.1"
BITCOIND_IMAGE="kylemanna/bitcoind@sha256:86bbcaa99bf3bf3d5df7fd6d9217b3a41194db4c4e34385064f2cfc9fa7bdf91"
GO_IMAGE="golang:${GO_VERSION}-bookworm"

usage() {
    cat <<EOF
Usage: $(basename "$0") [--check]

Update reviewed Go, Docker, CI action, and tooling dependency pins. The default
mode also updates all compatible Go modules and tidies go.mod/go.sum.

Options:
  --check  Verify that every managed text pin has the expected value.
  -h       Show this help.
EOF
}

MODE="update"
if [[ $# -gt 1 ]]; then
    usage >&2
    exit 1
fi
if [[ $# -eq 1 ]]; then
    case "$1" in
        --check)
            MODE="check"
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "Unknown argument: $1" >&2
            usage >&2
            exit 1
            ;;
    esac
fi

for cmd in python3 git; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo "Missing required command: $cmd" >&2
        exit 1
    fi
done

python3 - \
    "$REPO_ROOT" \
    "$MODE" \
    "$GO_VERSION" \
    "$ALPINE_VERSION" \
    "$STATICCHECK_VERSION" \
    "$BITCOIND_IMAGE" \
    "$NEUTRINO_VERSION" \
    "$NEUTRINO_FORK_VERSION" \
    "$BTCD_VERSION" <<'PY'
from __future__ import annotations

import pathlib
import re
import sys

(
    root_value,
    mode,
    go_version,
    alpine_version,
    staticcheck_version,
    bitcoind_image,
    neutrino_version,
    neutrino_fork_version,
    btcd_version,
) = sys.argv[1:]
root = pathlib.Path(root_value)

# Each count is deliberate. A mismatch means a workflow or managed file changed
# and this updater must be reviewed instead of silently leaving a stale pin.
rules: list[tuple[str, str, str, int]] = [
    ("neutrino_server/Dockerfile", r"^FROM golang:[^ ]+ AS builder$", f"FROM golang:{go_version}-alpine3.23 AS builder", 1),
    ("neutrino_server/Dockerfile", r"^FROM alpine:[^ ]+$", f"FROM alpine:{alpine_version}", 1),
    ("docker-compose.yml", r"(?m)^    image: kylemanna/bitcoind(?:[:@][^\n]+)$", f"    image: {bitcoind_image}", 1),
    ("scripts/release-build-sign.sh", r'^GO_VERSION="[^"]+"$', f'GO_VERSION="{go_version}"', 1),
    ("scripts/verify-release-build.sh", r'^GO_VERSION="[^"]+"$', f'GO_VERSION="{go_version}"', 1),
    (".pre-commit-config.yaml", r"honnef\.co/go/tools/cmd/staticcheck@v[0-9.]+", f"honnef.co/go/tools/cmd/staticcheck@{staticcheck_version}", 1),
    (".github/workflows/release.yaml", r"actions/checkout@v[0-9.]+", "actions/checkout@v7.0.1", 4),
    (".github/workflows/release.yaml", r"actions/setup-go@v[0-9.]+", "actions/setup-go@v7.0.0", 1),
    (".github/workflows/release.yaml", r"actions/upload-artifact@v[0-9.]+", "actions/upload-artifact@v7.0.1", 1),
    (".github/workflows/release.yaml", r"actions/download-artifact@v[0-9.]+", "actions/download-artifact@v8.0.1", 1),
    (".github/workflows/release.yaml", r"docker/setup-qemu-action@v[0-9.]+", "docker/setup-qemu-action@v4.2.0", 1),
    (".github/workflows/release.yaml", r"docker/setup-buildx-action@v[0-9.]+", "docker/setup-buildx-action@v4.3.0", 1),
    (".github/workflows/release.yaml", r"docker/login-action@v[0-9.]+", "docker/login-action@v4.6.0", 1),
    (".github/workflows/release.yaml", r"docker/metadata-action@v[0-9.]+", "docker/metadata-action@v6.2.0", 1),
    (".github/workflows/release.yaml", r"docker/build-push-action@v[0-9.]+", "docker/build-push-action@v7.3.0", 1),
    (".github/workflows/release.yaml", r"go-version: '[0-9.]+'", f"go-version: '{go_version}'", 1),
    (".github/workflows/test.yaml", r"actions/checkout@v[0-9.]+", "actions/checkout@v7.0.1", 3),
    (".github/workflows/test.yaml", r"actions/setup-go@v[0-9.]+", "actions/setup-go@v7.0.0", 1),
    (".github/workflows/test.yaml", r"codecov/codecov-action@v[0-9.]+", "codecov/codecov-action@v7.0.0", 1),
    (".github/workflows/test.yaml", r"go-version: '[0-9.]+'", f"go-version: '{go_version}'", 1),
    (".github/workflows/test.yaml", r"honnef\.co/go/tools/cmd/staticcheck@(?:latest|v[0-9.]+)", f"honnef.co/go/tools/cmd/staticcheck@{staticcheck_version}", 1),
    (".github/workflows/e2e-mainnet.yaml", r"actions/checkout@v[0-9.]+", "actions/checkout@v7.0.1", 1),
    (".github/workflows/e2e-mainnet.yaml", r"actions/setup-go@v[0-9.]+", "actions/setup-go@v7.0.0", 1),
    (".github/workflows/e2e-mainnet.yaml", r"actions/upload-artifact@v[0-9.]+", "actions/upload-artifact@v7.0.1", 1),
    (".github/workflows/e2e-mainnet.yaml", r"go-version: '[0-9.]+'", f"go-version: '{go_version}'", 1),
    (".github/workflows/pre-commit.yaml", r"actions/checkout@v[0-9.]+", "actions/checkout@v7.0.1", 1),
    (".github/workflows/pre-commit.yaml", r"actions/setup-go@v[0-9.]+", "actions/setup-go@v7.0.0", 1),
    (".github/workflows/pre-commit.yaml", r"actions/setup-python@v[0-9.]+", "actions/setup-python@v7.0.0", 1),
    (".github/workflows/pre-commit.yaml", r"pre-commit/action@v[0-9.]+", "pre-commit/action@v3.0.1", 1),
    (".github/workflows/pre-commit.yaml", r"go-version: '[0-9.]+'", f"go-version: '{go_version}'", 1),
    (".github/workflows/docker.yaml", r"actions/checkout@v[0-9.]+", "actions/checkout@v7.0.1", 1),
    (".github/workflows/docker.yaml", r"docker/setup-buildx-action@v[0-9.]+", "docker/setup-buildx-action@v4.3.0", 1),
    (".github/workflows/docker.yaml", r"docker/login-action@v[0-9.]+", "docker/login-action@v4.6.0", 1),
    (".github/workflows/docker.yaml", r"docker/metadata-action@v[0-9.]+", "docker/metadata-action@v6.2.0", 1),
    (".github/workflows/docker.yaml", r"docker/build-push-action@v[0-9.]+", "docker/build-push-action@v7.3.0", 1),
]

changed: list[str] = []
for relative_path, pattern, replacement, expected_count in rules:
    path = root / relative_path
    content = path.read_text(encoding="utf-8")
    updated, count = re.subn(pattern, replacement, content, flags=re.MULTILINE)
    if count != expected_count:
        raise SystemExit(
            f"{relative_path}: expected {expected_count} matches for {pattern!r}, found {count}"
        )
    if mode == "check" and content != updated:
        raise SystemExit(f"{relative_path}: managed dependency pin is stale")
    if mode == "update" and content != updated:
        path.write_text(updated, encoding="utf-8")
        changed.append(relative_path)

if mode == "check":
    go_mod = (root / "neutrino_server/go.mod").read_text(encoding="utf-8")
    expected_values = (
        f"go {go_version}",
        f"github.com/btcsuite/btcd {btcd_version}",
        f"github.com/lightninglabs/neutrino {neutrino_version}",
        f"github.com/m0wer/neutrino {neutrino_fork_version}",
    )
    for expected in expected_values:
        if expected not in go_mod:
            raise SystemExit(f"neutrino_server/go.mod: missing expected pin {expected!r}")
    print("All managed dependency pins are current.")
elif changed:
    print("Updated text pins in:")
    for relative_path in changed:
        print(f"  {relative_path}")
else:
    print("Managed text pins were already current.")
PY

if [[ "$MODE" == "check" ]]; then
    exit 0
fi

run_go() {
    if command -v docker >/dev/null 2>&1; then
        mkdir -p "$REPO_ROOT/tmp/dependency-cache/go-build" "$REPO_ROOT/tmp/dependency-cache/go-mod"
        chmod 0777 "$REPO_ROOT/tmp/dependency-cache/go-build" "$REPO_ROOT/tmp/dependency-cache/go-mod"
        docker run --rm \
            --user "$(id -u):$(id -g)" \
            -e GOCACHE=/workspace/tmp/dependency-cache/go-build \
            -e GOMODCACHE=/workspace/tmp/dependency-cache/go-mod \
            -e GOPATH=/tmp/go \
            -e GOWORK=off \
            -v "$REPO_ROOT:/workspace" \
            -w /workspace/neutrino_server \
            "$GO_IMAGE" \
            go "$@"
        return
    fi

    if ! command -v go >/dev/null 2>&1; then
        echo "Missing required command: docker or go" >&2
        exit 1
    fi
    local actual_version
    actual_version="$(go env GOVERSION)"
    if [[ "$actual_version" != "go${GO_VERSION}" ]]; then
        echo "Go version mismatch: expected go${GO_VERSION}, got ${actual_version}" >&2
        echo "Install Go ${GO_VERSION} or Docker to run updates with ${GO_IMAGE}." >&2
        exit 1
    fi
    (
        cd "$REPO_ROOT/neutrino_server"
        GOWORK=off go "$@"
    )
}

run_go mod edit -go="$GO_VERSION"
run_go mod edit -require="github.com/lightninglabs/neutrino@${NEUTRINO_VERSION}"
run_go mod edit -replace="github.com/lightninglabs/neutrino=github.com/m0wer/neutrino@${NEUTRINO_FORK_VERSION}"
run_go get "github.com/btcsuite/btcd@${BTCD_VERSION}"
run_go get -u ./...
run_go mod tidy
run_go mod verify

"$0" --check
git -C "$REPO_ROOT" diff --check

echo
echo "Dependency update complete. Review this diff before committing:"
git -C "$REPO_ROOT" status --short
git -C "$REPO_ROOT" diff --stat
