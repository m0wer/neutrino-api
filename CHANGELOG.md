# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.4.0] - 2025-12-21

### Fixed
- Fixed UTXO lookup endpoint to properly scan blocks using compact block filters
  - Replaced neutrino's `GetUtxo` method (which requires prior rescan) with manual block scanning
  - Now correctly finds UTXOs by scanning from `start_height` forward using BIP158 filters
  - Detects both UTXO creation and any subsequent spends in a single scan
  - Significantly improved performance when `start_height` is close to the block containing the transaction
- Updated README examples with correct data:
  - Fixed block header response for block 820000 with actual blockchain data
  - Updated UTXO lookup example with modern SegWit transaction instead of old P2PK format
  - Added performance guidance for UTXO lookups based on scan range

### Added
- New UTXO lookup endpoint (`GET /v1/utxo/{txid}/{vout}`) to check if a specific UTXO exists and whether it has been spent
  - Requires `address` query parameter (needed for BIP158 compact block filter matching)
  - Optional `start_height` query parameter to limit scan range
  - Returns spend information if UTXO was spent (spending txid, input index, block height)
- Test for missing address parameter validation on UTXO endpoint

### Changed
- README updated with UTXO endpoint documentation explaining why address is required
- Enhanced performance notes for UTXO lookups with concrete timing examples

## [0.3.0] - 2025-12-19

### Added
- Main entry point (`cmd/neutrinod/main.go`) for building the standalone binary
- Mainnet end-to-end tests (`e2e/mainnet_test.go`) that:
  - Build and run the actual neutrinod binary against mainnet in isolation
  - Use random available port to avoid conflicts with running instances
  - Create temporary data directory for each test run
  - Wait for blockchain sync to at least height 100,000
  - Verify API endpoints with real blockchain data (genesis block, block 100000, etc.)
  - Test address watching and UTXO queries with historical Bitcoin addresses
  - Properly cleanup server process and temporary files after tests
- GitHub workflow for automated mainnet e2e tests (`.github/workflows/e2e-mainnet.yaml`)
  - Runs on pushes to any branch that modify neutrino_server files
  - Can be triggered manually with configurable sync parameters

### Changed
- README updated with real Bitcoin addresses for examples:
  - Satoshi's address from block 9: `12cbQLTFMXRnSzktFkuoG3eHoMeFtpTu3S`
  - Hal Finney's address (first BTC recipient): `1Q2TWHE3GMdB6BZKafqwxXtWAWgFt5Jvm3`
- README now includes e2e test documentation with usage instructions
- Documented the need for `-count=1` flag to disable Go test caching for fresh e2e runs

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

[Unreleased]: https://github.com/yourusername/neutrino-api/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/yourusername/neutrino-api/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/yourusername/neutrino-api/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/yourusername/neutrino-api/releases/tag/v0.1.0
