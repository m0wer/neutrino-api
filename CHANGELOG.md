# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed
- Removed unnecessary nil check in rescan_test.go flagged by staticcheck SA4031
- Release workflow now requires pre-commit and test checks to pass before creating release

## [0.2.0] - 2025-12-19

### Added
- Full block filter-based rescan implementation using BIP157/BIP158 compact filters
- Comprehensive test suite for rescan functionality with 100% coverage of new features
- Enhanced logging throughout rescan operations for better debugging
- Asynchronous rescan execution to prevent blocking HTTP responses

### Changed
- RescanManager now uses key-based UTXO storage (`txid:vout`) instead of address-based lists
- Rescan endpoint now returns immediately with "started" status and runs in background
- RescanManager constructor now requires logger parameter for better observability
- UTXO set implementation improved for O(1) lookups and duplicate prevention

### Technical Details
- Rescan now fetches full blocks only when filters match, improving efficiency
- Tracks spent outputs during rescan to maintain accurate UTXO set
- Added extensive debug logging for block scanning, filter matching, and UTXO discovery

## [0.1.0] - 2025-12-19

### Added
- Initial Neutrino API Server implementation
- REST API with 9 comprehensive endpoints:
  - Status and sync monitoring (`/v1/status`)
  - Block header queries (`/v1/block/{height}/header`)
  - Filter header queries (`/v1/block/{height}/filter_header`)
  - Transaction broadcasting (`/v1/tx/broadcast`)
  - UTXO queries (`/v1/utxos`)
  - Address watching (`/v1/watch/address`)
  - Outpoint watching (`/v1/watch/outpoint`)
  - Blockchain rescanning (`/v1/rescan`)
  - Fee estimation (`/v1/fees/estimate`)
  - Peer management (`/v1/peers`)
- Docker support with multi-stage builds (13MB final image)
- Docker Compose configuration with Bitcoin Core regtest example
- Comprehensive test suite with unit and integration tests
- GitHub Actions CI/CD workflows:
  - Automated testing (Go 1.21)
  - Docker image building and pushing to GHCR
  - Multi-platform binary releases (Linux, macOS, Windows on amd64/arm64)
  - Pre-commit checks
  - Automated release workflow with checksums
- Pre-commit hooks for code quality (go fmt, vet, mod tidy, test)
- Support for all Bitcoin networks (mainnet, testnet, regtest, signet)
- Comprehensive documentation (README, VERSIONING, RELEASE, AGENTS guides)
- Version information via `--version` flag

### Technical Details
- Based on Neutrino v0.16.0
- Go 1.21
- Multi-architecture support (amd64, arm64)
- BIP157/BIP158 compact block filters
- Privacy-preserving SPV client
- RESTful JSON API
- Configurable via CLI flags or environment variables

[Unreleased]: https://github.com/yourusername/neutrino-api/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/yourusername/neutrino-api/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/yourusername/neutrino-api/releases/tag/v0.1.0
