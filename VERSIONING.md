# Versioning Strategy

This document describes the versioning strategy for the Neutrino API Server.

## Version Format

We use semantic versioning with a reference to the upstream Neutrino version:

```
v<MAJOR>.<MINOR>.<PATCH>-neutrino<UPSTREAM_VERSION>+build.<BUILD>
```

### Examples

- `v1.0.0-neutrino0.16.0` - First release based on Neutrino v0.16.0
- `v1.0.1-neutrino0.16.0` - Bug fix for our wrapper, still using Neutrino v0.16.0
- `v1.1.0-neutrino0.16.0` - New feature in our wrapper, still using Neutrino v0.16.0
- `v2.0.0-neutrino0.17.0` - Breaking change or upgrade to Neutrino v0.17.0

## Version Components

### MAJOR.MINOR.PATCH

This tracks **our** changes to the API wrapper:

- **MAJOR**: Breaking changes to the REST API or Docker configuration
- **MINOR**: New features, new API endpoints, non-breaking changes
- **PATCH**: Bug fixes, documentation updates, minor improvements

### Neutrino Version

The `-neutrino<VERSION>` suffix indicates which version of the underlying
[lightninglabs/neutrino](https://github.com/lightninglabs/neutrino) library
we're using.

### Build Number

Optional `+build.<NUMBER>` suffix for multiple builds of the same version
(e.g., different optimizations, security patches).

## Release Process

### 1. Update Version Constants

When preparing a release, update the version in:

```go
// neutrino_server/cmd/neutrinod/main.go
var (
    neutrinoVersion = "v0.16.0"  // Update this when upgrading neutrino
)
```

### 2. Create a Tag

Tags should follow the format `v<MAJOR>.<MINOR>.<PATCH>`:

```bash
git tag -a v1.0.0 -m "Release v1.0.0 based on Neutrino v0.16.0"
git push origin v1.0.0
```

### 3. Automated Release

The GitHub Actions workflow (`.github/workflows/release.yaml`) will:

1. Build binaries for multiple platforms (Linux, macOS, Windows on amd64/arm64)
2. Create Docker images for multiple architectures
3. Generate SHA256 checksums
4. Create a GitHub release with all artifacts

## Tracking Upstream Changes

### Neutrino Updates

When a new version of Neutrino is released:

1. Update `go.mod`:
   ```bash
   cd neutrino_server
   go get github.com/lightninglabs/neutrino@v0.17.0
   go mod tidy
   ```

2. Update the version constant in `main.go`:
   ```go
   neutrinoVersion = "v0.17.0"
   ```

3. Test thoroughly with all networks (mainnet, testnet, regtest, signet)

4. Create a new release (usually MAJOR or MINOR bump)

### Recording Changes

Maintain a `CHANGELOG.md` to track:
- Neutrino version upgrades
- New API endpoints
- Bug fixes
- Breaking changes
- Performance improvements

## Version Information

Users can check version information:

```bash
# From binary
./neutrinod --version

# From Docker
docker run neutrino-api:latest --version

# From API
curl http://localhost:8334/v1/status
```

## Build Tags

Recommended tagging strategy:

| Tag Pattern | Description | Example |
|-------------|-------------|---------|
| `v*.*.*` | Full release | `v1.2.3` |
| `v*.*.*-rc*` | Release candidate | `v1.2.3-rc1` |
| `v*.*.*-beta*` | Beta release | `v1.2.3-beta1` |
| `v*.*.*-alpha*` | Alpha release | `v1.2.3-alpha1` |

## Maintenance

### Long-term Support (LTS)

- Latest version receives active support
- Previous MAJOR version receives security updates for 6 months
- Older versions are community-supported

### Security Updates

Security patches are released as PATCH versions and backported to supported versions.

## References

- [Neutrino Releases](https://github.com/lightninglabs/neutrino/releases)
- [Semantic Versioning 2.0.0](https://semver.org/)
- [Keep a Changelog](https://keepachangelog.com/)
