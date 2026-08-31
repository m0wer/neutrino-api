# Neutrino API

A standalone REST API server for [Neutrino](https://github.com/lightninglabs/neutrino), a privacy-preserving Bitcoin light client using BIP157/BIP158 compact block filters.

## Features

- **Privacy-First**: Uses compact block filters (BIP157/158) for client-side filtering without revealing addresses to peers
- **Lightweight**: No need to download full blockchain, only block headers and compact filters
- **REST API**: Simple HTTP endpoints for blockchain queries, transaction broadcasting, and UTXO scanning
- **Multi-Network**: Support for mainnet, testnet, regtest, and signet
- **Docker Support**: Easy deployment with Docker and docker-compose (linux/amd64, linux/386, linux/arm64, linux/arm/v7, linux/arm/v6)
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

Pin to a major version tag (e.g., `:1`) for automatic updates without breaking changes:

```bash
# Run for mainnet (TLS + auth enabled by default)
docker run -d \
  -p 8334:8334 \
  -v neutrino-data:/data/neutrino \
  -e NETWORK=mainnet \
  -e LOG_LEVEL=info \
  ghcr.io/m0wer/neutrino-api:1

# Run for regtest with auth disabled
docker run -d \
  -p 8334:8334 \
  -v neutrino-data:/data/neutrino \
  -e NETWORK=regtest \
  -e ADD_PEERS=bitcoin-node:18444 \
  -e LOG_LEVEL=debug \
  -e NO_AUTH=true \
  ghcr.io/m0wer/neutrino-api:1

# Run for signet (using conventional signet API port 38334)
docker run -d \
  -p 38334:38334 \
  -v neutrino-signet-data:/data/neutrino \
  -e NETWORK=signet \
  -e LISTEN_ADDR=0.0.0.0:38334 \
  -e LOG_LEVEL=info \
  -e ADD_PEERS=bitcoin.sgn.space:38333 \
  ghcr.io/m0wer/neutrino-api:1
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

## Reproducible Releases

Release binaries are reproducible and tied to a signed digest:

1. Build locally and sign checksums:

```bash
./scripts/release-build-sign.sh v1.0.0 --key 1C53A412D11EF3051704419C44912E1E03005B31
```

2. Commit `signatures/v1.0.0/SHA256SUMS` and `signatures/v1.0.0/SHA256SUMS.asc`.
3. Push the release tag (`v1.0.0`).

The release workflow rebuilds all binaries, verifies the resulting `SHA256SUMS` exactly matches the committed digest, verifies the GPG signature using keys in `signatures/pubkeys/`, and uploads binaries + `SHA256SUMS` + `SHA256SUMS.asc` to the GitHub release.

Anyone can reproduce and verify a release locally with one command:

```bash
./scripts/verify-release-build.sh v1.0.0
```

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `NETWORK` | `mainnet` | Bitcoin network (mainnet, testnet, regtest, signet) |
| `LISTEN_ADDR` | `0.0.0.0:8334` | REST API listen address (for signet, `0.0.0.0:38334` is a common convention) |
| `DATA_DIR` | `/data/neutrino` | Data directory for headers and filters |
| `LOG_LEVEL` | `info` | Log level (trace, debug, info, warn, error) |
| `ADD_PEERS` | | Comma-separated list of preferred peers (e.g., `node1:8333,node2:8333`) while still allowing peer discovery |
| `TOR_PROXY` | | Tor SOCKS5 proxy address (e.g., `127.0.0.1:9050`) |
| `CLEARNET_INITIAL_SYNC` | `true` | When `TOR_PROXY` is set, perform initial public header sync over clearnet before switching to Tor |
| `PREFETCH_FILTERS` | `true` | Prefetch compact filter bodies in the background so historical scans do not block on peer downloads. Set to `false` to minimize storage when historical UTXO lookups are not needed |
| `PREFETCH_WORKERS` | `0` | Number of background prefetch workers (`0` selects an automatic value) |
| `PREFETCH_START` | `0` | First height to prefetch. `0` derives the start from `PREFETCH_LOOKBACK` |
| `PREFETCH_LOOKBACK` | `105120` | Number of blocks to prefetch when `PREFETCH_START=0` (about two years) |
| `CFILTER_CDN_AUTO` | `true` | Download prefetched compact filters from block-dn after P2P header sync (verified against filter headers) |
| `CFILTER_CDN_URL` | | Override block-dn base URL for compact filter CDN downloads |
| `AUTO_SYNC_WATCHED` | `true` | Continuously scan new blocks for watched addresses in the background, keeping the UTXO set up-to-date so `/v1/utxos` is instant. Reacts to block-connected notifications from the chain service in real time |
| `AUTO_SYNC_INTERVAL_SEC` | `30` | Fallback poll interval (in seconds) used while waiting for initial header sync, and as a safety net if block-notification subscription is unavailable. Only used when `AUTO_SYNC_WATCHED=true` |
| `MEMPOOL_ENABLED` | `true` | Enable the watched-only mempool tracker. The daemon subscribes to every connected peer's incoming `inv` messages, fetches each announced tx, and records the ones that pay or spend a watched address. Unconfirmed UTXOs are surfaced in `/v1/utxos` (with `height: 0`) and unconfirmed spends are overlaid on `/v1/utxo/{txid}/{vout}`. Disable with `MEMPOOL_ENABLED=false` to keep the chain-only behaviour |
| `TX_HISTORY_ENABLED` | `true` | Persist confirmed watched-transaction records during scanning so clients can reconstruct wallet history via `GET /v1/transactions`. Advertised as `tx_history_enabled` in `/v1/status`. Disable with `TX_HISTORY_ENABLED=false` |
| `AUTO_RECOVER_HEADER_CACHE` | `true` | Detect an inconsistent Neutrino header index/flat-file cache at startup, preserve the rebuildable cache in a timestamped recovery directory, and resync public chain data from genesis. TLS, auth, watched addresses, UTXOs, and transaction history are preserved |
| `MAX_PEERS` | `8` | Maximum number of peers to connect to |
| `NO_AUTH` | `false` | Disable TLS and token authentication (for development/regtest) |

### Command Line Flags

```bash
./neutrinod \
  --network=mainnet \
  --listen=0.0.0.0:8334 \
  --datadir=/data/neutrino \
  --loglevel=info \
  --addpeer=peer1:8333,peer2:8333 \
  --torproxy=127.0.0.1:9050 \
  --clearnet-initial-sync=true \
  --prefetchfilters=true \
  --prefetchlookback=105120 \
  --cfilter-cdn-auto=true \
  --maxpeers=8 \
  --mempool=true \
  --auto-recover-header-cache=true \
  --no-auth        # Disable TLS + auth (dev/regtest only)
  # --reset-auth   # Regenerate TLS cert and auth token, then exit
```

## Using with Tor

Neutrino supports routing all Bitcoin P2P connections through Tor for enhanced privacy. This prevents peers from learning your IP address.

### Docker Compose with Tor

```yaml
services:
  tor:
    image: ghcr.io/m0wer/docker-tor:latest
    container_name: tor
    restart: unless-stopped
    ports:
      - "9050:9050"

  neutrino:
    image: ghcr.io/m0wer/neutrino-api:1
    container_name: neutrino
    restart: unless-stopped
    environment:
      - NETWORK=mainnet
      - LISTEN_ADDR=0.0.0.0:8334
      - TOR_PROXY=tor:9050
      - LOG_LEVEL=info
    ports:
      - "8334:8334"
    volumes:
      - neutrino-data:/data/neutrino
    depends_on:
      - tor
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:8334/v1/status"]
      interval: 30s
      timeout: 10s
      retries: 3

volumes:
  neutrino-data:
```

### Using Docker Run

```bash
# Start Tor proxy
docker run -d --name tor -p 9050:9050 ghcr.io/m0wer/docker-tor:latest

# Run neutrino with Tor
docker run -d \
  -p 8334:8334 \
  -v neutrino-data:/data/neutrino \
  -e NETWORK=mainnet \
  -e TOR_PROXY=host.docker.internal:9050 \
  -e LOG_LEVEL=info \
  ghcr.io/m0wer/neutrino-api:1
```

**Note:** When running Tor and neutrino in separate containers, use `host.docker.internal:9050` (on macOS/Windows) or `--network host` (on Linux) to access the Tor proxy.

### Using Local Tor Installation

If you have Tor installed locally:

```bash
# Start Tor (default SOCKS5 proxy on 127.0.0.1:9050)
tor

# Run neutrino (single-phase Tor mode: P2P and CDN traffic via Tor)
./neutrinod \
  --network=mainnet \
  --torproxy=127.0.0.1:9050 \
  --clearnet-initial-sync=false \
  --cfilter-cdn-auto=true
```

By default (`PREFETCH_FILTERS=true`, `CFILTER_CDN_AUTO=true`), the server
downloads the configured compact-filter lookback from block-dn after P2P
header sync completes. Filters are verified against P2P-synced filter headers
before storage. Set `PREFETCH_FILTERS=false` for deployments that do not need
historical scans and prefer to minimize disk usage. Supported networks:

- mainnet: `https://block-dn.org`
- signet: `https://signet.block-dn.org`
- testnet3: `https://testnet3.block-dn.org`

If `CFILTER_CDN_URL` is set, it overrides the auto-resolved URL. CDN
downloads are routed through Tor when a SOCKS5 proxy is configured.

When `TOR_PROXY` and `CLEARNET_INITIAL_SYNC=true` are both set (default),
P2P header sync runs over clearnet in phase 1, then the chain service
restarts with Tor for privacy-sensitive operations (filter fetches, block
downloads, broadcasts).

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
  "peers": 8,
  "mempool_enabled": true,
  "tx_history_enabled": true,
  "mempool": {
    "entries": 4,
    "utxos": 3,
    "spends": 1,
    "peers": 8
  }
}
```

The `mempool` object is omitted when `MEMPOOL_ENABLED=false`. `entries` counts watched mempool txs, `utxos` counts unconfirmed outputs paying watched addresses, `spends` counts unconfirmed spends of watched outpoints, and `peers` reflects how many connected peers the tracker is subscribed to. `tx_history_enabled` reflects whether confirmed transaction history is being persisted for `GET /v1/transactions`.

### Block Header

Get block header by height:

```bash
curl http://localhost:8334/v1/block/820000/header
```

Response:
```json
{
  "hash": "00000000000000000000ba232574c32b4f0cd023e133c05125310625626d6571",
  "height": 820000,
  "timestamp": 1701860856,
  "version": 827375616,
  "prev_block": "000000000000000000002660d26de87c900f770430d209814b238d15b17a0cfe",
  "merkle_root": "e19b5e3ecaee81f04acd80b5298de8d8e0744aee9e88835dd07c42e478d2a3d4",
  "bits": 386147408,
  "nonce": 3717997606
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

### Remove Watched Addresses

Remove watched addresses in one request, along with their persisted confirmed
UTXOs:

```bash
curl -X DELETE http://localhost:8334/v1/watch/addresses \
  -H "Content-Type: application/json" \
  -d '{"addresses": ["12cbQLTFMXRnSzktFkuoG3eHoMeFtpTu3S"]}'
```

Response:

```json
{
  "status": "ok",
  "removed_addresses": 1,
  "removed_utxos": 2
}
```

The request must contain between 1 and 1,000 address entries. Duplicate
addresses are processed once. All addresses are validated before any state is
changed, and a removal during an active rescan is rejected with HTTP `409`.
Repeated removals succeed with zero counts. The operation preserves shared
rescan coverage metadata and confirmed transaction history. It also removes
matching watched mempool outputs and spends of the confirmed UTXOs removed by
the request, without clearing unrelated mempool entries.

### Get UTXOs

Query UTXOs for a list of addresses (requires prior rescan to populate UTXO set):

```bash
# First, do a rescan to populate the UTXO set for your addresses
curl -X POST http://localhost:8334/v1/rescan \
  -H "Content-Type: application/json" \
  -d '{
    "start_height": 0,
    "addresses": ["12cbQLTFMXRnSzktFkuoG3eHoMeFtpTu3S"]
  }'

# Then query UTXOs (mempool entries are included by default)
curl -X POST http://localhost:8334/v1/utxos \
  -H "Content-Type: application/json" \
  -d '{"addresses": ["12cbQLTFMXRnSzktFkuoG3eHoMeFtpTu3S"]}'

# Suppress unconfirmed mempool entries
curl -X POST http://localhost:8334/v1/utxos \
  -H "Content-Type: application/json" \
  -d '{"addresses": ["12cbQLTFMXRnSzktFkuoG3eHoMeFtpTu3S"], "include_mempool": false}'
```

> **Note:** When `AUTO_SYNC_WATCHED=true` (the default), the daemon
> subscribes to block-connected notifications and incrementally scans
> every new block for all watched addresses in the background. After
> the initial rescan, subsequent `/v1/utxos` queries return immediately
> without requiring another `/v1/rescan` — the daemon stays caught up
> in real time and also re-syncs on every restart.

> **Mempool:** When `MEMPOOL_ENABLED=true` (the default), unconfirmed
> outputs paying a watched address are returned in the same `utxos`
> array with `height: 0`. Set `include_mempool: false` in the request
> body to opt out and receive only confirmed UTXOs. If the same outpoint
> appears in both sets (e.g., the mempool tracker has not yet evicted a
> just-confirmed tx), the confirmed entry wins.

Response:
```json
{
  "utxos": [
    {
      "txid": "0437cd7f8525ceed2324359c2d0ba26006d92d856a9c20fa0241106ee5a597c9",
      "vout": 0,
      "value": 5000000000,
      "address": "12cbQLTFMXRnSzktFkuoG3eHoMeFtpTu3S",
      "scriptpubkey": "410411db93e1dcdb8a016b49840f8c53bc1eb68a382e97b1482ecad7b148a6909a5cb2e0eaddfb84ccf9744464f82e160bfa9b8b64f9d4c03f999b8643f656b412a3ac",
      "height": 9
    },
    {
      "txid": "ea44e97271691990157559d0bdd9959e02790c34db6c006d779e82fa5aee708e",
      "vout": 1,
      "value": 12345,
      "address": "12cbQLTFMXRnSzktFkuoG3eHoMeFtpTu3S",
      "scriptpubkey": "76a914...",
      "height": 0
    }
  ]
}
```

### Check UTXO Status

Check if a specific UTXO exists and whether it has been spent. This endpoint requires knowing the address that owns the UTXO, because neutrino uses compact block filters (BIP158) which match on scripts/addresses, not transaction outpoints.

```bash
# Check a recent UTXO status
# Required: address - the Bitcoin address that owns/owned this output
# Optional: start_height - block height to start scanning from (highly recommended for performance)
curl "http://localhost:8334/v1/utxo/4b36c31dacf6a1b72cfd9cece16813001921b14f4413dce9278899d218a25044/0?address=bc1qs8efrjj5nrkfgxcpfll5wxfqrwngjww4vxdggs&start_height=928819"
```

Response for unspent UTXO:
```json
{
  "unspent": true,
  "value": 11516,
  "scriptpubkey": "001481f291ca5498ec941b014fff4719201ba68939d5"
}
```

Response for spent UTXO:
```json
{
  "unspent": false,
  "spending_txid": "a1b2c3d4e5f6...",
  "spending_input": 0,
  "spending_height": 928820
}
```

Response for an on-chain unspent UTXO that has an unconfirmed spend in the
mempool (when `MEMPOOL_ENABLED=true`, default):
```json
{
  "unspent": true,
  "value": 11516,
  "scriptpubkey": "001481f291ca5498ec941b014fff4719201ba68939d5",
  "mempool_spending_txid": "ccccdddd...",
  "mempool_spending_input": 0,
  "mempool_spend_first_seen": 1714501234
}
```

Append `?include_mempool=false` to suppress the mempool overlay and receive
only the on-chain status. A confirmed spend always takes precedence over
any tracked mempool spend.

**Important Notes**:
- The `address` parameter is **required**. Compact block filters (BIP158) work by matching on scripts, not transaction IDs. Without the address, filter matching cannot work correctly.
- Specifying a `start_height` parameter is **highly recommended** for performance. Set it to the block height where the UTXO was created (or slightly before). Without it, the scan could take a very long time as it scans from the provided height to the current chain tip.
- The `start_height` means "start scanning FROM this height going FORWARD to the chain tip", not backwards.
- Performance scales with the scan range: scanning 1 block takes ~0.01s, scanning 100 blocks takes ~0.5s, scanning 10,000+ blocks can take minutes.
- A single-UTXO lookup has a 25-second server deadline. It returns `503 Service Unavailable` when a required block hash, compact filter, or block cannot be retrieved, and `504 Gateway Timeout` when that lookup deadline expires. Both responses are retryable; an `unspent` response is returned only after every block through the captured chain tip has been checked.
- UTXOs for watched wallet addresses return immediately from the persisted rescan state when its coverage reaches the current chain tip. Stale cached state is never used to answer an `unspent` query.

### Get Transaction (mempool only)

Fetch a serialized transaction by txid. With `MEMPOOL_ENABLED=true` (default)
the daemon returns the watched mempool tx if it has been observed; for any
other txid it responds with `501 Not Implemented` because compact block
filters do not allow looking up arbitrary historical transactions without a
full block download.

```bash
curl http://localhost:8334/v1/tx/<txid>
```

Response when the tx is in the watched mempool:
```json
{
  "txid": "ccccdddd...",
  "hex": "0200000001...",
  "mempool": true
}
```

### Get Transaction History

Enumerate transactions that touched a watched address. Returns confirmed
records with block height strictly greater than `since_height` (default `0`),
plus every current mempool entry, each with raw hex. Poll incrementally by
passing back the returned `cursor` as the next `since_height`.

Requires `TX_HISTORY_ENABLED=true` (default); check `tx_history_enabled` in
`/v1/status`.

```bash
curl "http://localhost:8334/v1/transactions?since_height=820000"
```

Response:
```json
{
  "transactions": [
    {
      "txid": "aaaa1111...",
      "hex": "0200000001...",
      "height": 820005,
      "confirmed": true,
      "addresses": ["bc1q..."],
      "direction": "receive"
    },
    {
      "txid": "bbbb2222...",
      "hex": "0200000002...",
      "height": 0,
      "confirmed": false,
      "addresses": ["bc1q..."],
      "direction": "spend",
      "first_seen": 1720000000
    }
  ],
  "cursor": 820005
}
```

`direction` is `receive` (the tx created a watched output), `spend` (it spent a
watched outpoint), or `receive,spend`. `confirmed: false` entries come from the
mempool (`height: 0`, with a `first_seen` Unix timestamp). `cursor` is the
highest confirmed height returned (or the requested `since_height` when there
were none). History is best-effort: only transactions in blocks that matched
the compact filter for a watched script are recorded, and on a reorg re-scan
records at or above the re-scan height are pruned (clients dedupe by txid).

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

Rescans normally skip ranges already covered by persisted global scan state.
When adding addresses after that scan, set `"force": true` to evaluate the
requested historical range against the new scripts instead of resuming from
the persisted tip:

```bash
curl -X POST http://localhost:8334/v1/rescan \
  -H "Content-Type: application/json" \
  -d '{
    "start_height": 0,
    "addresses": ["12cbQLTFMXRnSzktFkuoG3eHoMeFtpTu3S"],
    "force": true
  }'
```

`GET /v1/rescan/status` advertises this capability as
`"force_rescan_supported": true`. Forced rescans can be expensive on long
chains and should be limited to newly watched addresses.

Rescan requests are admitted synchronously and serialized: when the POST
returns `{"status": "started"}` the status endpoint already reports
`in_progress: true`, and a request that overlaps a running scan is rejected
with HTTP `409`. Asynchronous scan failures are surfaced through the status
endpoint's `last_error` field. A forced scan over an explicit address subset
does not modify the persisted `last_start_height`/`last_scanned_tip`
coverage metadata, because only that subset was evaluated over the range.

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
    image: ghcr.io/m0wer/neutrino-api:1
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

volumes:
  neutrino-data:
```

### systemd Service Example (binary install)

If you run the standalone binary directly on a host (including Raspberry Pi),
this unit file starts `neutrinod` at boot and restarts it on failures:

```ini
# /etc/systemd/system/neutrinod.service
[Unit]
Description=Neutrino API daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=neutrino
Group=neutrino
Environment=NETWORK=signet
Environment=LISTEN_ADDR=0.0.0.0:38334
Environment=DATA_DIR=/var/lib/neutrinod
Environment=LOG_LEVEL=info
Environment=ADD_PEERS=bitcoin.sgn.space:38333
ExecStart=/usr/local/bin/neutrinod --network=${NETWORK} --listen=${LISTEN_ADDR} --datadir=${DATA_DIR} --loglevel=${LOG_LEVEL} --addpeer=${ADD_PEERS}
Restart=always
RestartSec=5
NoNewPrivileges=true
ProtectSystem=full
ProtectHome=true
ReadWritePaths=/var/lib/neutrinod

[Install]
WantedBy=multi-user.target
```

```bash
sudo useradd --system --home /var/lib/neutrinod --shell /usr/sbin/nologin neutrino
sudo mkdir -p /var/lib/neutrinod
sudo chown -R neutrino:neutrino /var/lib/neutrinod
sudo systemctl daemon-reload
sudo systemctl enable --now neutrinod
```

**Note:** TLS and API token authentication are enabled by default. After first start, find the auth token in the data volume at `auth_token` and the TLS certificate at `tls.cert`. Clients must use HTTPS and include `Authorization: Bearer <token>`.

### Security Considerations

- **TLS + auth enabled by default**: The server auto-generates a self-signed TLS certificate and API token on first start. Clients must use HTTPS and present the token via `Authorization: Bearer <token>`.
- To retrieve the auth token from a Docker container: `docker exec neutrino cat /data/neutrino/auth_token`
- To retrieve the TLS cert for client pinning: `docker cp neutrino:/data/neutrino/tls.cert ./tls.cert`
- Set `NO_AUTH=true` only for development/regtest environments.
- Use `--reset-auth` to rotate credentials and clear privacy data (watched addresses, UTXOs).
- Run as non-root user (already configured in Dockerfile)
- Monitor resource usage and set appropriate limits
- Keep data directory backed up
- After a recovered header-cache resync succeeds, remove old `header-cache-recovery-*` directories to reclaim space.
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
