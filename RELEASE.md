# Release Guide

Quick reference for creating releases of the Neutrino API Server.

## Prerequisites

- Push access to the repository
- All tests passing on main branch
- Updated CHANGELOG.md

## Release Steps

### 1. Prepare Release

```bash
# Update CHANGELOG.md with release notes
vim CHANGELOG.md

# Commit changes
git add CHANGELOG.md
git commit -m "Prepare release v1.0.0"
git push origin main
```

### 2. Create and Push Tag

```bash
# Create annotated tag
git tag -a v1.0.0 -m "Release v1.0.0

Built with Neutrino v0.16.0

Changes:
- Feature 1
- Feature 2
- Bug fix 3
"

# Push tag to trigger release workflow
git push origin v1.0.0
```

### 3. Monitor Release

The GitHub Actions workflow will automatically:

1. **Build Binaries** for:
   - Linux (amd64, arm64)
   - macOS (amd64, arm64)
   - Windows (amd64)

2. **Build Docker Images** for:
   - linux/amd64
   - linux/arm64

3. **Create GitHub Release** with:
   - All binary archives
   - SHA256SUMS file
   - Release notes

4. **Push Docker Images** to:
   - `ghcr.io/yourusername/neutrino-api:v1.0.0`
   - `ghcr.io/yourusername/neutrino-api:v1.0`
   - `ghcr.io/yourusername/neutrino-api:v1`
   - `ghcr.io/yourusername/neutrino-api:latest` (if main branch)

Monitor at: `https://github.com/yourusername/neutrino-api/actions`

### 4. Verify Release

```bash
# Check GitHub release page
open https://github.com/yourusername/neutrino-api/releases

# Test binary download
wget https://github.com/yourusername/neutrino-api/releases/download/v1.0.0/neutrinod-linux-amd64.tar.gz
tar -xzf neutrinod-linux-amd64.tar.gz
./neutrinod-linux-amd64 --version

# Test Docker image
docker pull ghcr.io/yourusername/neutrino-api:v1.0.0
docker run --rm ghcr.io/yourusername/neutrino-api:v1.0.0 neutrinod --version
```

### 5. Announce Release

- Update project README if needed
- Post in relevant communities
- Update documentation site (if applicable)

## Release Types

### Patch Release (v1.0.0 → v1.0.1)

Bug fixes, documentation updates:

```bash
git tag -a v1.0.1 -m "Release v1.0.1 - Bug fixes"
git push origin v1.0.1
```

### Minor Release (v1.0.0 → v1.1.0)

New features, non-breaking changes:

```bash
git tag -a v1.1.0 -m "Release v1.1.0 - New features"
git push origin v1.1.0
```

### Major Release (v1.0.0 → v2.0.0)

Breaking changes, API changes:

```bash
git tag -a v2.0.0 -m "Release v2.0.0 - Breaking changes"
git push origin v2.0.0
```

## Pre-release Versions

### Release Candidate

```bash
git tag -a v1.1.0-rc1 -m "Release v1.1.0-rc1"
git push origin v1.1.0-rc1
```

### Beta Release

```bash
git tag -a v1.1.0-beta1 -m "Release v1.1.0-beta1"
git push origin v1.1.0-beta1
```

## Upgrading Neutrino Version

When a new upstream Neutrino version is released:

```bash
# 1. Update dependencies
cd neutrino_server
go get github.com/lightninglabs/neutrino@v0.17.0
go mod tidy

# 2. Update version constant
vim cmd/neutrinod/main.go
# Change: neutrinoVersion = "v0.17.0"

# 3. Test thoroughly
go test ./...
docker compose up -d --build
# ... test all networks ...

# 4. Update CHANGELOG
vim ../CHANGELOG.md
# Add: Upgraded to Neutrino v0.17.0

# 5. Create release
git add .
git commit -m "Upgrade to Neutrino v0.17.0"
git push origin main
git tag -a v2.0.0 -m "Release v2.0.0 - Neutrino v0.17.0"
git push origin v2.0.0
```

## Troubleshooting

### Release workflow fails

1. Check workflow logs: `https://github.com/yourusername/neutrino-api/actions`
2. Common issues:
   - Tests failing: Fix and push new commit
   - Build errors: Check Go version, dependencies
   - Docker errors: Check Dockerfile, permissions

### Need to delete/recreate tag

```bash
# Delete local tag
git tag -d v1.0.0

# Delete remote tag
git push origin :refs/tags/v1.0.0

# Create new tag
git tag -a v1.0.0 -m "Release v1.0.0"
git push origin v1.0.0
```

### Rollback release

If a release has critical issues:

1. Mark GitHub release as pre-release
2. Create hotfix branch
3. Fix issue
4. Create new patch release (e.g., v1.0.1)
5. Deprecate old release in notes

## Checklist

Before creating a release:

- [ ] All tests passing on main
- [ ] CHANGELOG.md updated
- [ ] Documentation updated
- [ ] Version number follows semver
- [ ] Neutrino version documented
- [ ] Breaking changes documented (if major release)
- [ ] Migration guide provided (if major release)
- [ ] Pre-commit hooks passing
- [ ] Docker image builds locally
- [ ] API tested manually

After creating a release:

- [ ] GitHub release created
- [ ] Binaries downloadable
- [ ] Docker images pushed
- [ ] SHA256SUMS verified
- [ ] Release notes accurate
- [ ] Latest tag points to correct version
- [ ] Documentation reflects new version
