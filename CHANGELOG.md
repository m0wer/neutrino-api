# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Add a production-ready `systemd` service example in README for running the
  `neutrinod` binary directly on Linux hosts (including Raspberry Pi), with a
  signet-oriented configuration example.

### Changed

- Docker image publishing now includes `linux/386`, `linux/arm/v7`, and
  `linux/arm/v6` in addition to `linux/amd64` and `linux/arm64`, expanding
  compatibility to 32-bit x86 and broader 32-bit ARM Linux systems.
- Release binary matrix now includes `linux-386`, `linux-armv7`,
  `linux-armv6`, and `windows-arm64` artifacts in addition to the previous
  `linux-amd64`, `linux-arm64`, `darwin-amd64`, `darwin-arm64`, and
  `windows-amd64` outputs.
- README now documents a signet Docker run example using
  `LISTEN_ADDR=0.0.0.0:38334` and host port mapping `38334:38334`.

## [1.2.0] - 2026-04-30

### Fixed

- **Preserve earliest `last_start_height` across incremental rescans.**
  Previously, every call to `Rescan()` (including auto-sync passes that
  start at `last_scanned_tip + 1`) overwrote the persisted
  `last_start_height` with the most recent invocation's start. This
  destroyed the wallet's coverage record on disk: clients use
  `last_start_height` together with `last_scanned_tip` to decide whether
  the daemon has already scanned the requested lookback window and skip
  redundant full rescans. The bug forced a full re-scan on every CLI
  invocation, which manifested as a ~12 s rescan even when the daemon
  had already scanned the entire requested range. `LastStartHeight` is
  now only lowered (when the new start is earlier or no scan has ever
  run); incremental scans no longer narrow the recorded coverage.

### Added

- **Continuous background sync of watched addresses (`AUTO_SYNC_WATCHED`).**
  The daemon now subscribes to block-connected notifications from the chain
  service and incrementally scans every new block for all watched addresses
  in the background, keeping the persisted UTXO set up-to-date in real time.
  After the initial `/v1/rescan`, subsequent `/v1/utxos` queries return
  immediately without requiring clients to re-trigger a rescan, and the
  daemon also catches up on every restart so wallet startup is instant
  even after extended downtime.
  - New config: `AUTO_SYNC_WATCHED` / `--auto-sync-watched` (default: `true`)
    enables the auto-sync goroutine.
  - New config: `AUTO_SYNC_INTERVAL_SEC` / `--auto-sync-interval` (default:
    `30`) sets the fallback poll interval used while waiting for initial
    header sync and as a safety net if block-notification subscription is
    unavailable.
  - Auto-sync skips when a manual rescan is already running and only starts
    after the chain service reports `IsCurrent() == true` to avoid scanning
    against an incomplete header chain.

## [1.1.0] - 2026-04-09

### Changed

- **Replace HTTP header import with CDN compact filter (cfilter) prefetch.**
  Block headers and filter headers are now synced exclusively via P2P (trusted),
  while compact block filters can be bulk-downloaded from a block-dn CDN and
  verified against the P2P-synced filter headers before storage. This provides
  a better trust/performance balance: headers come from P2P consensus, filters
  are cryptographically verified against those headers, and the CDN is used
  only as untrusted transport for large filter data.
  - New config: `CFILTER_CDN_AUTO` / `--cfilter-cdn-auto` (default: `true`)
    enables automatic cfilter download from block-dn after P2P header sync.
  - New config: `CFILTER_CDN_URL` / `--cfilter-cdn-url` overrides the
    auto-resolved block-dn base URL.
  - Removed config: `HEADER_IMPORT_AUTO`, `HEADER_IMPORT_BLOCK_URL`,
    `HEADER_IMPORT_FILTER_URL` and their corresponding CLI flags.
  - Removed header import fallback retry logic from chain service startup.
  - CDN filters are downloaded in 2000-filter chunks, parsed from CompactSize
    varint binary format, and verified via `ChainService.ImportCFilters()`.
  - Add `ImportCFilter()` and `ImportCFilters()` methods to the neutrino fork
    for verified bulk cfilter import.

### Fixed

- **CDN chunk bounds now floor-align to include the containing chunk.**
  Previously, when the prefetch start height did not align to a chunk boundary,
  the first available chunk was skipped, leaving a gap that required P2P
  fallback. The start is now floor-aligned so the chunk containing the
  requested start height is always downloaded.
- **CDN prefetch continues past failed chunks instead of aborting.**
  Previously, any CDN chunk failure (verification mismatch or HTTP error)
  would abort the entire CDN prefetch, forcing P2P to fetch all remaining
  filters. Now, failed chunks are retried with exponential backoff, and if
  retries are exhausted the chunk is skipped. CDN continues with subsequent
  chunks, and P2P fills only the specific gaps. CDN prefetch is aborted
  only after 3 consecutive failures.
- **Pin neutrino fork dependency to pushed commit.**
  Replace local path `replace` directive with versioned pseudo-version
  pointing to `m0wer/neutrino@c1b598b97446`.

## [1.0.1] - 2026-04-08

### Fixed

- Fix `.onion` peer hostname handling in ban checks by patching the neutrino
  ban manager parser to support Tor v2/v3 addresses, so discovered onion peers
  no longer trigger `unsupported IP type` parse errors.

## [1.0.0] - 2026-04-07

### Added

- **Auto-TLS and API token authentication**: The server now generates a self-signed TLS certificate (EC P-256) and a random API token on first start, protecting against eavesdropping and unauthorized access. Credentials are persisted in the data directory.
- Add `--no-auth` / `NO_AUTH=true` flag to disable TLS and token authentication (for development/regtest environments).
- Add `--reset-auth` flag to regenerate TLS certificate and auth token, and clear privacy-sensitive data (watched addresses, UTXOs).
- Add `GET /v1/version` endpoint returning `{"version": "..."}` for lightweight version diagnostics.
- Include `version` and `watched_addresses` fields in `GET /v1/status` responses.
- Include `watched_addresses` and `server_version` in `GET /v1/rescan/status` responses.
- Add `X-Neutrino-Version` response header on JSON API responses.
- **Docker image major-version tags** (e.g., `ghcr.io/m0wer/neutrino-api:1`): Users can pin to a major version for automatic minor/patch updates without breaking changes.

### Changed

- **BREAKING: Server now uses HTTPS and requires authentication by default.** Previously the server listened on plain HTTP with no authentication. It now auto-generates a TLS certificate and auth token, and requires `Authorization: Bearer <token>` on all requests. See the migration guide below.

### Migration guide (upgrading to this version)

Existing deployments that relied on unauthenticated plain HTTP access will break after upgrading. Choose one of the following approaches:

1. **Adopt TLS + auth (recommended for production):** After starting the upgraded server, find the auto-generated auth token in `<datadir>/auth_token`. Update your clients to use HTTPS and include the `Authorization: Bearer <token>` header. If using self-signed TLS, configure your client to trust `<datadir>/tls.cert`.

2. **Disable auth (development/regtest only):** Set `NO_AUTH=true` (environment variable) or pass `--no-auth` to restore the previous unauthenticated HTTP behavior. This is already set in the provided `docker-compose.yml` for the regtest environment.

3. **Pin your Docker image to a major version tag** to avoid future breaking changes from automatic pulls. Use `ghcr.io/m0wer/neutrino-api:0` for the pre-auth behavior or `ghcr.io/m0wer/neutrino-api:1` once v1.0.0 is released.

## [0.10.0] - 2026-04-06

### Added

- **Two-phase startup for faster initial sync with Tor setups**: Added `--clearnet-initial-sync` / `CLEARNET_INITIAL_SYNC` (default: `true`). When Tor is configured, the node now syncs block headers and filter headers over clearnet first, then restarts the chain service in Tor mode for privacy-sensitive operations.

### Changed

- **Compact filter prefetch is now opt-in**: `--prefetchfilters` / `PREFETCH_FILTERS` now defaults to `false` to avoid downloading and storing the full historical filter set on first startup.
- **New prefetch lookback control**: Added `--prefetchlookback` / `PREFETCH_LOOKBACK` (default: `105120`, about 2 years). When prefetch is enabled and `prefetchstart=0`, the prefetch start height is auto-computed as `tip - lookback`.

### Fixed

- **Spent UTXO detection in bulk endpoint**: `POST /v1/utxos` could return already-spent UTXOs because the batch `MatchAny` filter scan missed spending blocks in certain cases. Added a per-UTXO spend verification pass after the main scan that uses single-script `filter.Match` (the same approach used by the reliable `GET /v1/utxo/{txid}/{vout}` endpoint) to catch any spends missed by the batch scan.

## [0.9.0] - 2026-03-19

### Added

- **Persistent state across restarts** (`rescan_state.db`): Watched addresses, UTXO set, and rescan metadata (last scanned tip, start height) are now persisted to a separate bbolt database. On restart, the server restores its previous state so UTXOs are available immediately without re-scanning. The state store uses three buckets (`watched_addrs`, `utxo_set`, `rescan_meta`) and is optional (nil = no persistence) for backward compatibility with tests.
- **Incremental rescan**: When a rescan is requested with a `start_height` within the already-scanned range, the scan starts from `LastScannedTip+1` instead of re-scanning the entire range. If already up-to-date, returns immediately. This avoids redundant 52k+ block rescans after restarts.
- **Rescan fallback to watched addresses**: `POST /v1/rescan` with an empty `addresses` field now falls back to all previously watched addresses instead of silently doing nothing.
- **HTTP request/response logging middleware**: Every API call is logged with method, path, status code, and duration. 4xx/5xx responses log at WARN level; 2xx at INFO level.
- **Rescan progress logging**: During block scanning, progress is logged every 10 seconds with blocks scanned/total, percentage, current height, blocks/sec, estimated time remaining, and filter match count. A final summary includes total blocks, duration, speed, filter matches, and detailed UTXO accounting (found/added/removed).
- **Rescan handler request logging**: `POST /v1/rescan` now logs start_height, address count, and outpoint count at INFO level.

## [0.8.0] - 2026-03-17

### Added

- Add reproducible release flow for `neutrinod` binaries with deterministic Go build flags and `SOURCE_DATE_EPOCH`.
- Add `scripts/release-build-sign.sh` to build release binaries locally, generate `SHA256SUMS`, and create a detached GPG signature.
- Add `scripts/verify-release-build.sh` as a one-command local reproducibility check against the signed digest.
- Add release signature infrastructure under `signatures/` with trusted key list and m0wer public key.
- Add `addPeers` setting, similar to `connectPeers`, that allows specifying peers to connect to without disabling peer discovery.

### Changed

- Update release workflow to rebuild binaries in CI, verify checksums against committed `signatures/<version>/SHA256SUMS`, verify `SHA256SUMS.asc`, and upload binaries plus signed digest files to GitHub releases.

## [0.7.0] - 2026-03-11

### Added

- Add `GET /v1/rescan/status` endpoint that returns `{"in_progress": bool}`, allowing clients to poll until a background rescan completes instead of using a fixed sleep.
- Add `block_height` to `UTXOSpendReport` for more efficient scans.

## [0.6.1] - 2026-01-09

### Fixed
- Fixed UTXO endpoint to return HTTP 404 (Not Found) instead of 500 (Internal Server Error) when UTXO is not found
- Added proper error type handling for API errors:
  - `NotFoundError` returns HTTP 404
  - `BadRequestError` returns HTTP 400
  - Invalid address or txid parameters now return 400 instead of 500

## [0.6.0] - 2026-01-08

### Removed
- Fee estimation endpoint (`/v1/fees/estimate`) - Neutrino light clients don't have access to mempool data anyway, so this endpoint was misleading

## [0.5.0] - 2025-12-30

### Added
- Tor support for enhanced privacy (inspired by LND's implementation)
  - Added `TOR_PROXY` environment variable and `--torproxy` command-line flag
  - All Bitcoin P2P connections are routed through Tor SOCKS5 proxy
  - DNS resolution performed through Tor using `connmgr.TorLookupIP` (prevents DNS leaks)
  - Prevents peers from learning the node's IP address
  - Full support for .onion addresses (Tor v3 hidden services)
  - .onion addresses preserved as encoded bytes to maintain hostname through neutrino's address manager
  - Regular DNS names properly resolved through Tor (returns actual IPs, not dummy values)
  - Compatible with standard Tor installations (default: 127.0.0.1:9050)
  - Docker Compose example with Tor proxy integration
  - Comprehensive documentation with usage examples
  - Note: Neutrino may log cosmetic "unsupported IP type" warnings for .onion addresses, but connections work perfectly

### Changed
- Updated Go version requirement to 1.25 (from 1.21) for latest dependency compatibility
- Updated Docker base image to golang:1.25-alpine
- Updated all CI/CD workflows to use Go 1.25

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
- REST API with 8 comprehensive endpoints:
  - Status and sync monitoring (`/v1/status`)
  - Block header queries (`/v1/block/{height}/header`)
  - Filter header queries (`/v1/block/{height}/filter_header`)
  - Transaction broadcasting (`/v1/tx/broadcast`)
  - UTXO queries (`/v1/utxos`)
  - Address watching (`/v1/watch/address`)
  - Outpoint watching (`/v1/watch/outpoint`)
  - Blockchain rescanning (`/v1/rescan`)
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

[Unreleased]: https://github.com/m0wer/neutrino-api/compare/v1.2.0...HEAD
[1.2.0]: https://github.com/m0wer/neutrino-api/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/m0wer/neutrino-api/compare/v1.0.1...v1.1.0
[1.0.1]: https://github.com/m0wer/neutrino-api/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/m0wer/neutrino-api/compare/v0.10.0...v1.0.0
[0.10.0]: https://github.com/m0wer/neutrino-api/compare/v0.9.0...v0.10.0
[0.9.0]: https://github.com/m0wer/neutrino-api/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/m0wer/neutrino-api/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/m0wer/neutrino-api/compare/v0.6.1...v0.7.0
[0.6.1]: https://github.com/m0wer/neutrino-api/compare/v0.6.0...v0.6.1
[0.6.0]: https://github.com/m0wer/neutrino-api/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/m0wer/neutrino-api/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/m0wer/neutrino-api/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/m0wer/neutrino-api/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/m0wer/neutrino-api/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/m0wer/neutrino-api/releases/tag/v0.1.0
