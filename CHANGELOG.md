# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Initial Neutrino API Server implementation
- REST API with comprehensive endpoints:
  - Status and sync monitoring (`/v1/status`)
  - Block header queries (`/v1/block/{height}/header`)
  - Transaction broadcasting (`/v1/tx/broadcast`)
  - UTXO queries (`/v1/utxos`)
  - Address watching (`/v1/watch/address`)
  - Blockchain rescanning (`/v1/rescan`)
  - Fee estimation (`/v1/fees/estimate`)
  - Peer management (`/v1/peers`)
- Docker support with multi-stage builds
- Docker Compose configuration with Bitcoin Core regtest example
- Comprehensive test suite
- GitHub Actions CI/CD workflows:
  - Automated testing
  - Docker image building
  - Binary releases for multiple platforms
  - Pre-commit checks
- Pre-commit hooks for code quality
- Support for all Bitcoin networks (mainnet, testnet, regtest, signet)
- Comprehensive documentation

### Technical Details
- Based on Neutrino v0.16.0
- Go 1.21
- Multi-architecture support (amd64, arm64)
- BIP157/BIP158 compact block filters

## [0.1.0] - TBD

### Added
- First official release

[Unreleased]: https://github.com/yourusername/neutrino-api/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/yourusername/neutrino-api/releases/tag/v0.1.0
