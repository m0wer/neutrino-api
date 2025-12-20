# Neutrino API

A standalone REST API server for [Neutrino](https://github.com/lightninglabs/neutrino), a privacy-preserving Bitcoin light client using BIP157/BIP158 compact block filters.

## Features

- **Privacy-First**: Uses compact block filters (BIP157/158) for client-side filtering without revealing addresses to peers
- **Lightweight**: No need to download full blockchain, only block headers and compact filters
- **REST API**: Simple HTTP endpoints for blockchain queries, transaction broadcasting, and UTXO scanning
- **Multi-Network**: Support for mainnet, testnet, regtest, and signet
- **Docker Support**: Easy deployment with Docker and docker-compose
- **Production Ready**: Includes health checks, graceful shutdown, and comprehensive logging

## Quick Start

### Using Docker Compose

The easiest way to get started:

```bash
# Start neutrino with a local Bitcoin node (regtest)
docker compose up -d

# Check if neutrino is synced
curl -s http://localhost:8334/v1/status | jq
```

### Using Docker

```bash
# Build the image
docker build -t neutrino-api ./neutrino_server

# Run for mainnet
docker run -d \
  -p 8334:8334 \
  -v neutrino-data:/data/neutrino \
  -e NETWORK=mainnet \
  -e LOG_LEVEL=info \
  ghcr.io/m0wer/neutrino-api

# Run for regtest with custom Bitcoin node
docker run -d \
  -p 8334:8334 \
  -v neutrino-data:/data/neutrino \
  -e NETWORK=regtest \
  -e CONNECT_PEERS=bitcoin-node:18444 \
  -e LOG_LEVEL=debug \
  ghcr.io/m0wer/neutrino-api
```

### Building from Source

```bash
cd neutrino_server

# Install dependencies
go mod download

# Build
go build -o neutrinod ./cmd/neutrinod

# Run
./neutrinod --network=mainnet --listen=0.0.0.0:8334
```

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `NETWORK` | `mainnet` | Bitcoin network (mainnet, testnet, regtest, signet) |
| `LISTEN_ADDR` | `0.0.0.0:8334` | REST API listen address |
| `DATA_DIR` | `/data/neutrino` | Data directory for headers and filters |
| `LOG_LEVEL` | `info` | Log level (trace, debug, info, warn, error) |
| `CONNECT_PEERS` | | Comma-separated list of peers (e.g., `node1:8333,node2:8333`) |
| `MAX_PEERS` | `8` | Maximum number of peers to connect to |

### Command Line Flags

```bash
./neutrinod \
  --network=mainnet \
  --listen=0.0.0.0:8334 \
  --datadir=/data/neutrino \
  --loglevel=info \
  --connect=peer1:8333,peer2:8333 \
  --maxpeers=8
```

## API Reference

### Status

Get current node status and sync progress:

```bash
curl http://localhost:8334/v1/status
```

Response:
```json
{
  "synced": true,
  "block_height": 820000,
  "filter_height": 820000,
  "peers": 8
}
```

### Block Header

Get block header by height:

```bash
curl http://localhost:8334/v1/block/820000/header
```

Response:
```json
{
  "hash": "00000000000000000002a7c4c1e48d76c5a37902165a270156b7a8d72728a054",
  "height": 820000,
  "timestamp": 1702000000,
  "version": 536870912,
  "prev_block": "00000000000000000003c2e5e7f3c4b0d7f2e8a1d5c3b6f4e7a9d8c1b2a5e6f3",
  "merkle_root": "a7c4d8e2f5b9c3e6f8a1d4b7e9c2f5a8b3d6e9f2c5a8b1d4e7f9c2e5a8b3d6e9",
  "bits": 386089497,
  "nonce": 1234567890
}
```

### Broadcast Transaction

Broadcast a raw transaction to the network:

```bash
curl -X POST http://localhost:8334/v1/tx/broadcast \
  -H "Content-Type: application/json" \
  -d '{"tx_hex": "0200000001..."}'
```

Response:
```json
{
  "txid": "a7c4d8e2f5b9c3e6f8a1d4b7e9c2f5a8b3d6e9f2c5a8b1d4e7f9c2e5a8b3d6e9"
}
```

### Watch Address

Add an address to watch for transactions:

```bash
# Watch Satoshi's known address from block 9
curl -X POST http://localhost:8334/v1/watch/address \
  -H "Content-Type: application/json" \
  -d '{"address": "12cbQLTFMXRnSzktFkuoG3eHoMeFtpTu3S"}'
```

### Get UTXOs

Query UTXOs for a list of addresses:

```bash
# Query UTXOs for historical Bitcoin addresses
# (requires prior rescan to populate UTXO set)
curl -X POST http://localhost:8334/v1/utxos \
  -H "Content-Type: application/json" \
  -d '{"addresses": ["12cbQLTFMXRnSzktFkuoG3eHoMeFtpTu3S", "1Q2TWHE3GMdB6BZKafqwxXtWAWgFt5Jvm3"]}'
```

Response:
```json
{
  "utxos": [
    {
      "txid": "f4184fc596403b9d638783cf57adfe4c75c605f6356fbc91338530e9831e9e16",
      "vout": 0,
      "value": 5000000000,
      "address": "1Q2TWHE3GMdB6BZKafqwxXtWAWgFt5Jvm3",
      "scriptpubkey": "76a914...",
      "height": 170
    }
  ]
}
```

### Rescan

Trigger a blockchain rescan from a specific height:

```bash
# Rescan from block 0 for Satoshi's address
curl -X POST http://localhost:8334/v1/rescan \
  -H "Content-Type: application/json" \
  -d '{
    "start_height": 0,
    "addresses": ["12cbQLTFMXRnSzktFkuoG3eHoMeFtpTu3S"]
  }'
```

### Fee Estimation

Get estimated fee rate for target confirmation blocks:

```bash
curl "http://localhost:8334/v1/fees/estimate?target_blocks=6"
```

Response:
```json
{
  "fee_rate": 5,
  "target_blocks": 6
}
```

### Peers

Get connected peer information:

```bash
curl http://localhost:8334/v1/peers
```

Response:
```json
{
  "peers": [],
  "count": 8
}
```

## Development

### Running Tests

```bash
cd neutrino_server

# Run unit tests
go test ./...

# Run tests with coverage
go test -v -race -coverprofile=coverage.out ./...

# View coverage report
go tool cover -html=coverage.out

# Run mainnet e2e tests (requires network access, ~15-20 min)
# Note: Use -count=1 to disable test caching and force a fresh run
go test -tags=e2e -v -count=1 -timeout 30m ./e2e/...
```

The e2e tests will:
1. Build and start the neutrinod binary on a random available port
2. Use a fresh temporary data directory for each run
3. Connect to mainnet peers and sync block headers/filters
4. Verify API endpoints with real blockchain data (genesis block, block 100000, etc.)
5. Test address watching and UTXO queries
6. Properly cleanup the server process and temporary files

**Note:** Go caches test results by default. To force a fresh run every time, use the `-count=1` flag as shown above.

### Building Docker Image

```bash
docker build -t neutrino-api:latest ./neutrino_server
```

## Production Deployment

### Docker Compose Example

```yaml
services:
  neutrino:
    image: ghcr.io/m0wer/neutrino-api
    container_name: neutrino
    restart: unless-stopped
    environment:
      - NETWORK=mainnet
      - LISTEN_ADDR=0.0.0.0:8334
      - DATA_DIR=/data/neutrino
      - LOG_LEVEL=info
      - MAX_PEERS=16
    ports:
      - "8334:8334"
    volumes:
      - neutrino-data:/data/neutrino
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:8334/v1/status"]
      interval: 30s
      timeout: 10s
      retries: 3

volumes:
  neutrino-data:
```

### Security Considerations

- Run as non-root user (already configured in Dockerfile)
- Use reverse proxy (nginx, Caddy) for TLS termination
- Implement rate limiting for API endpoints
- Monitor resource usage and set appropriate limits
- Keep data directory backed up
- Use firewall rules to restrict access

## Architecture

```
┌─────────────────┐
│   REST API      │
│   (HTTP/JSON)   │
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│  API Handler    │
│  (Gorilla Mux)  │
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│  Neutrino Node  │
│  (BIP157/158)   │
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│  Bitcoin P2P    │
│  Network        │
└─────────────────┘
```

## How It Works

1. **Block Headers**: Neutrino downloads and validates all block headers (80 bytes each)
2. **Compact Filters**: Downloads compact block filters for each block (typically ~20KB per block)
3. **Client-Side Filtering**: Matches addresses/scripts locally without revealing them to peers
4. **Privacy Preserved**: Only requests full blocks when filter indicates a potential match
5. **REST API**: Exposes blockchain data and operations via simple HTTP endpoints

## Resources

- [BIP 157 - Compact Block Filters](https://github.com/bitcoin/bips/blob/master/bip-0157.mediawiki)
- [BIP 158 - Compact Block Filters for SPV](https://github.com/bitcoin/bips/blob/master/bip-0158.mediawiki)
- [Neutrino GitHub](https://github.com/lightninglabs/neutrino)
- [Bitcoin Developer Documentation](https://developer.bitcoin.org/)

## License

MIT License - See [LICENSE](LICENSE) file for details
