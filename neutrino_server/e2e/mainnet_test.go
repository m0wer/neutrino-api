//go:build e2e
// +build e2e

/*
Package e2e provides end-to-end tests for the neutrino API server.

These tests build and run the actual binary against mainnet, wait for sync,
and verify that API endpoints return correct data for known blockchain data.

Run with: go test -tags=e2e -v -count=1 -timeout 30m ./e2e/...

IMPORTANT: Use -count=1 to disable Go's test caching and force a fresh run.
Without it, Go will reuse cached results and you'll see old logs.

The tests require network access and may take 15-20 minutes to complete
as they need to sync blockchain headers and filters to height 100,000+.

Each test run:
- Uses a random available port to avoid conflicts
- Creates a fresh temporary data directory
- Builds and runs a new binary
- Properly cleans up processes and files afterward
*/
package e2e

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	// Timeouts
	defaultSyncTimeout = 15 * time.Minute // Max time to wait for initial sync
	syncPollInterval   = 5 * time.Second
	requestTimeout     = 30 * time.Second
	startupTimeout     = 30 * time.Second

	// Minimum sync requirements
	defaultMinBlockHeight = 100000 // Wait until at least this height before running tests
	minPeers              = 1      // Need at least one peer
)

type e2eConfig struct {
	SyncTimeout    time.Duration
	MinBlockHeight int32
}

// Known mainnet data for verification
var (
	// Genesis block (height 0)
	genesisBlockHash = "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f"

	// Block 100000 - a well-known block
	block100000Hash = "000000000003ba27aa200b1cecaad478d2b00432346c3f1f3986da1afd33e506"

	// Block 500000 - another milestone
	block500000Hash = "00000000000000000024fb37364cbf81fd49cc2d51c09c75c35433c3a1945d04"

	// Well-known Bitcoin addresses with historical transactions
	// Satoshi's known address from block 9
	satoshiAddress = "12cbQLTFMXRnSzktFkuoG3eHoMeFtpTu3S"

	// First ever Bitcoin transaction recipient (Hal Finney) - block 170
	halFinneyAddress = "1Q2TWHE3GMdB6BZKafqwxXtWAWgFt5Jvm3"

	// The Bitcoin genesis block timestamp
	genesisTimestamp = int64(1231006505)
)

// StatusResponse represents the /v1/status response
type StatusResponse struct {
	Synced       bool  `json:"synced"`
	BlockHeight  int32 `json:"block_height"`
	FilterHeight int32 `json:"filter_height"`
	Peers        int   `json:"peers"`
}

// BlockHeaderResponse represents the /v1/block/{height}/header response
type BlockHeaderResponse struct {
	Hash       string `json:"hash"`
	Height     int64  `json:"height"`
	Timestamp  int64  `json:"timestamp"`
	Version    int32  `json:"version"`
	PrevBlock  string `json:"prev_block"`
	MerkleRoot string `json:"merkle_root"`
	Bits       uint32 `json:"bits"`
	Nonce      uint32 `json:"nonce"`
}

// PeersResponse represents the /v1/peers response
type PeersResponse struct {
	Peers []any `json:"peers"`
	Count int   `json:"count"`
}

// UTXOsResponse represents the /v1/utxos response
type UTXOsResponse struct {
	UTXOs []struct {
		TxID         string `json:"txid"`
		Vout         uint32 `json:"vout"`
		Value        int64  `json:"value"`
		Address      string `json:"address"`
		ScriptPubKey string `json:"scriptpubkey"`
		Height       int32  `json:"height"`
	} `json:"utxos"`
}

// WatchResponse represents the /v1/watch/address response
type WatchResponse struct {
	Status string `json:"status"`
}

// TestMainnetE2E is the main test function that sets up the server and runs all e2e tests
func TestMainnetE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping e2e test in short mode")
	}

	cfg := loadE2EConfig(t)

	// Get a random available port
	port, err := getFreePort()
	if err != nil {
		t.Fatalf("Failed to get free port: %v", err)
	}
	listenAddr := fmt.Sprintf("127.0.0.1:%d", port)
	baseURL := "https://" + listenAddr
	t.Logf("Using port %d for test server", port)

	// Build the binary
	binaryPath, err := buildBinary(t)
	if err != nil {
		t.Fatalf("Failed to build binary: %v", err)
	}
	defer os.Remove(binaryPath)

	// Create temp data directory
	dataDir, err := os.MkdirTemp("", "neutrino-e2e-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dataDir)

	// Start the server
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := startServer(ctx, t, binaryPath, dataDir, listenAddr)
	defer func() {
		t.Log("Stopping server...")
		if cmd.Process != nil {
			done := make(chan error, 1)
			go func() {
				done <- cmd.Wait()
			}()

			if err := signalServerProcessGroup(cmd, syscall.SIGTERM); err != nil {
				t.Logf("Failed to send SIGTERM: %v", err)
			}

			select {
			case err := <-done:
				if err != nil {
					t.Logf("Server exited with error: %v", err)
				}
				t.Log("Server stopped gracefully")
			case <-time.After(5 * time.Second):
				t.Log("Server did not stop gracefully, killing...")
				if err := signalServerProcessGroup(cmd, syscall.SIGKILL); err != nil {
					t.Logf("Failed to send SIGKILL: %v", err)
					return
				}

				select {
				case err := <-done:
					if err != nil {
						t.Logf("Server exited with error after SIGKILL: %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Log("Server did not exit within 5s after SIGKILL")
				}
			}
		}
	}()

	client, authToken, err := buildAuthenticatedClient(dataDir)
	if err != nil {
		t.Fatalf("Failed to create authenticated client: %v", err)
	}

	// Wait for server to be ready
	if err := waitForServer(t, client, baseURL, authToken); err != nil {
		t.Fatalf("Server failed to start: %v", err)
	}

	// Wait for initial sync
	if err := waitForSync(t, client, baseURL, authToken, cfg); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	// Run the actual tests
	t.Run("Status", func(t *testing.T) { testStatus(t, client, baseURL, authToken, cfg.MinBlockHeight) })
	t.Run("Peers", func(t *testing.T) { testPeers(t, client, baseURL, authToken) })
	t.Run("GenesisBlock", func(t *testing.T) { testGenesisBlock(t, client, baseURL, authToken) })
	t.Run("Block100000", func(t *testing.T) { testBlock100000(t, client, baseURL, authToken) })
	t.Run("Block500000", func(t *testing.T) { testBlock500000(t, client, baseURL, authToken) })
	t.Run("WatchAddress", func(t *testing.T) { testWatchAddress(t, client, baseURL, authToken) })
	t.Run("UTXOs", func(t *testing.T) { testUTXOs(t, client, baseURL, authToken) })
}

func loadE2EConfig(t *testing.T) e2eConfig {
	t.Helper()

	minBlockHeight := defaultMinBlockHeight
	if raw := strings.TrimSpace(os.Getenv("MIN_BLOCK_HEIGHT")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("invalid MIN_BLOCK_HEIGHT %q: %v", raw, err)
		}
		if parsed < 0 || parsed > math.MaxInt32 {
			t.Fatalf("MIN_BLOCK_HEIGHT out of range: %d", parsed)
		}
		minBlockHeight = parsed
	}

	syncTimeout := defaultSyncTimeout
	if raw := strings.TrimSpace(os.Getenv("SYNC_TIMEOUT")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			t.Fatalf("invalid SYNC_TIMEOUT %q: %v", raw, err)
		}
		if parsed <= 0 {
			t.Fatalf("SYNC_TIMEOUT must be > 0, got %s", raw)
		}
		syncTimeout = parsed
	}

	cfg := e2eConfig{SyncTimeout: syncTimeout, MinBlockHeight: int32(minBlockHeight)}
	t.Logf("E2E config: MIN_BLOCK_HEIGHT=%d, SYNC_TIMEOUT=%s", cfg.MinBlockHeight, cfg.SyncTimeout)
	return cfg
}

// buildBinary builds the neutrinod binary for testing
func buildBinary(t *testing.T) (string, error) {
	t.Helper()
	t.Log("Building neutrinod binary...")

	// Get the path to the source
	srcPath := filepath.Join("..", "cmd", "neutrinod")

	// Create a temp file for the binary
	tmpFile, err := os.CreateTemp("", "neutrinod-e2e-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpFile.Close()
	binaryPath := tmpFile.Name()

	// Build the binary
	cmd := exec.Command("go", "build", "-o", binaryPath, srcPath)
	cmd.Dir = filepath.Dir(srcPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(binaryPath)
		return "", fmt.Errorf("build failed: %w\nOutput: %s", err, output)
	}

	t.Logf("Binary built at: %s", binaryPath)
	return binaryPath, nil
}

// getFreePort asks the kernel for a free open port that is ready to use.
func getFreePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}

	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// startServer starts the neutrinod server
func startServer(ctx context.Context, t *testing.T, binaryPath, dataDir, listenAddr string) *exec.Cmd {
	t.Helper()
	t.Logf("Starting server with data dir: %s", dataDir)

	cmd := exec.CommandContext(ctx, binaryPath,
		"--network=mainnet",
		"--listen="+listenAddr,
		"--datadir="+dataDir,
		"--loglevel=info",
	)

	// Redirect output to test logs
	cmd.Stdout = &testLogWriter{t: t, prefix: "[SERVER] "}
	cmd.Stderr = &testLogWriter{t: t, prefix: "[SERVER] "}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}

	t.Logf("Server started with PID: %d", cmd.Process.Pid)
	return cmd
}

func signalServerProcessGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	// Signal the entire process group so any child processes do not outlive the parent.
	if err := syscall.Kill(-cmd.Process.Pid, sig); err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}

	return cmd.Process.Signal(sig)
}

// testLogWriter writes to test logs with a prefix
type testLogWriter struct {
	t      *testing.T
	prefix string
}

func (w *testLogWriter) Write(p []byte) (n int, err error) {
	lines := strings.Split(strings.TrimRight(string(p), "\n"), "\n")
	for _, line := range lines {
		if line != "" {
			w.t.Log(w.prefix + line)
		}
	}
	return len(p), nil
}

// waitForServer waits for the HTTPS server to be ready.
func waitForServer(t *testing.T, client *http.Client, baseURL, authToken string) error {
	t.Helper()
	t.Log("Waiting for server to be ready...")

	deadline := time.Now().Add(startupTimeout)

	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, baseURL+"/v1/status", nil)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		req.Header.Set("Authorization", "Bearer "+authToken)

		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			t.Log("Server is ready")
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("server did not become ready within %v", startupTimeout)
}

func buildAuthenticatedClient(dataDir string) (*http.Client, string, error) {
	certPath := filepath.Join(dataDir, "tls.cert")
	tokenPath := filepath.Join(dataDir, "auth_token")

	deadline := time.Now().Add(startupTimeout)
	for time.Now().Before(deadline) {
		certPEM, certErr := os.ReadFile(certPath)
		tokenBytes, tokenErr := os.ReadFile(tokenPath)
		if certErr == nil && tokenErr == nil {
			token := strings.TrimSpace(string(tokenBytes))
			if token == "" {
				time.Sleep(200 * time.Millisecond)
				continue
			}

			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(certPEM) {
				return nil, "", fmt.Errorf("failed to parse TLS certificate at %s", certPath)
			}

			transport := &http.Transport{
				TLSClientConfig: &tls.Config{
					MinVersion: tls.VersionTLS12,
					RootCAs:    pool,
				},
			}

			client := &http.Client{Timeout: requestTimeout, Transport: transport}
			return client, token, nil
		}

		time.Sleep(200 * time.Millisecond)
	}

	return nil, "", fmt.Errorf("timed out waiting for TLS cert and auth token in %s", dataDir)
}

// waitForSync waits for the node to sync to a minimum height
func waitForSync(t *testing.T, client *http.Client, baseURL, authToken string, cfg e2eConfig) error {
	t.Helper()
	t.Logf("Waiting for sync to height %d...", cfg.MinBlockHeight)

	deadline := time.Now().Add(cfg.SyncTimeout)
	lastHeight := int32(0)

	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, baseURL+"/v1/status", nil)
		if err != nil {
			t.Logf("Failed to build status request: %v", err)
			time.Sleep(syncPollInterval)
			continue
		}
		req.Header.Set("Authorization", "Bearer "+authToken)

		resp, err := client.Do(req)
		if err != nil {
			t.Logf("Status request failed: %v", err)
			time.Sleep(syncPollInterval)
			continue
		}

		var status StatusResponse
		if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
			resp.Body.Close()
			t.Logf("Failed to decode status: %v", err)
			time.Sleep(syncPollInterval)
			continue
		}
		resp.Body.Close()

		// Log progress
		if status.BlockHeight != lastHeight {
			t.Logf("Sync progress: height=%d, peers=%d, synced=%v",
				status.BlockHeight, status.Peers, status.Synced)
			lastHeight = status.BlockHeight
		}

		// Check if we have enough sync progress
		if status.BlockHeight >= cfg.MinBlockHeight && status.Peers >= minPeers {
			t.Logf("Sync complete: height=%d, peers=%d", status.BlockHeight, status.Peers)
			return nil
		}

		time.Sleep(syncPollInterval)
	}

	return fmt.Errorf("sync did not complete within %v (last height: %d)", cfg.SyncTimeout, lastHeight)
}

// HTTP helpers

func getJSON(t *testing.T, client *http.Client, baseURL, path, authToken string, result any) error {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+authToken)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, body)
	}

	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	return nil
}

func postJSON(t *testing.T, client *http.Client, baseURL, path, authToken string, body string, result any) error {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+path, strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, respBody)
	}

	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	return nil
}

// Individual test cases

func testStatus(t *testing.T, client *http.Client, baseURL, authToken string, minBlockHeight int32) {
	var status StatusResponse
	if err := getJSON(t, client, baseURL, "/v1/status", authToken, &status); err != nil {
		t.Fatalf("Failed to get status: %v", err)
	}

	t.Logf("Status: synced=%v, height=%d, filter_height=%d, peers=%d",
		status.Synced, status.BlockHeight, status.FilterHeight, status.Peers)

	if status.BlockHeight < minBlockHeight {
		t.Errorf("Block height %d is less than expected minimum %d", status.BlockHeight, minBlockHeight)
	}

	if status.Peers < minPeers {
		t.Errorf("Peer count %d is less than expected minimum %d", status.Peers, minPeers)
	}
}

func testPeers(t *testing.T, client *http.Client, baseURL, authToken string) {
	var peers PeersResponse
	if err := getJSON(t, client, baseURL, "/v1/peers", authToken, &peers); err != nil {
		t.Fatalf("Failed to get peers: %v", err)
	}

	t.Logf("Peers: count=%d", peers.Count)

	if peers.Count < minPeers {
		t.Errorf("Peer count %d is less than expected minimum %d", peers.Count, minPeers)
	}
}

func testGenesisBlock(t *testing.T, client *http.Client, baseURL, authToken string) {
	var header BlockHeaderResponse
	if err := getJSON(t, client, baseURL, "/v1/block/0/header", authToken, &header); err != nil {
		t.Fatalf("Failed to get genesis block header: %v", err)
	}

	t.Logf("Genesis block: hash=%s, timestamp=%d", header.Hash, header.Timestamp)

	// Verify the genesis block hash
	if header.Hash != genesisBlockHash {
		t.Errorf("Genesis block hash mismatch:\n  got:  %s\n  want: %s", header.Hash, genesisBlockHash)
	}

	// Verify height
	if header.Height != 0 {
		t.Errorf("Genesis block height should be 0, got %d", header.Height)
	}

	// Verify timestamp
	if header.Timestamp != genesisTimestamp {
		t.Errorf("Genesis block timestamp mismatch:\n  got:  %d\n  want: %d", header.Timestamp, genesisTimestamp)
	}

	// Verify prev_block is all zeros (genesis has no previous block)
	expectedPrevBlock := "0000000000000000000000000000000000000000000000000000000000000000"
	if header.PrevBlock != expectedPrevBlock {
		t.Errorf("Genesis prev_block should be all zeros, got %s", header.PrevBlock)
	}
}

func testBlock100000(t *testing.T, client *http.Client, baseURL, authToken string) {
	var header BlockHeaderResponse
	if err := getJSON(t, client, baseURL, "/v1/block/100000/header", authToken, &header); err != nil {
		t.Fatalf("Failed to get block 100000 header: %v", err)
	}

	t.Logf("Block 100000: hash=%s, timestamp=%d", header.Hash, header.Timestamp)

	// Verify the block hash
	if header.Hash != block100000Hash {
		t.Errorf("Block 100000 hash mismatch:\n  got:  %s\n  want: %s", header.Hash, block100000Hash)
	}

	// Verify height
	if header.Height != 100000 {
		t.Errorf("Block height should be 100000, got %d", header.Height)
	}
}

func testBlock500000(t *testing.T, client *http.Client, baseURL, authToken string) {
	// First check if we're synced high enough
	var status StatusResponse
	if err := getJSON(t, client, baseURL, "/v1/status", authToken, &status); err != nil {
		t.Fatalf("Failed to get status: %v", err)
	}

	if status.BlockHeight < 500000 {
		t.Skipf("Skipping block 500000 test - current height %d is too low", status.BlockHeight)
	}

	var header BlockHeaderResponse
	if err := getJSON(t, client, baseURL, "/v1/block/500000/header", authToken, &header); err != nil {
		t.Fatalf("Failed to get block 500000 header: %v", err)
	}

	t.Logf("Block 500000: hash=%s, timestamp=%d", header.Hash, header.Timestamp)

	// Verify the block hash
	if header.Hash != block500000Hash {
		t.Errorf("Block 500000 hash mismatch:\n  got:  %s\n  want: %s", header.Hash, block500000Hash)
	}

	// Verify height
	if header.Height != 500000 {
		t.Errorf("Block height should be 500000, got %d", header.Height)
	}
}

func testWatchAddress(t *testing.T, client *http.Client, baseURL, authToken string) {
	// Watch Satoshi's address
	body := fmt.Sprintf(`{"address": "%s"}`, satoshiAddress)
	var resp WatchResponse
	if err := postJSON(t, client, baseURL, "/v1/watch/address", authToken, body, &resp); err != nil {
		t.Fatalf("Failed to watch address: %v", err)
	}

	t.Logf("Watch address response: status=%s", resp.Status)

	if resp.Status != "ok" {
		t.Errorf("Expected status 'ok', got '%s'", resp.Status)
	}
}

func testUTXOs(t *testing.T, client *http.Client, baseURL, authToken string) {
	// Query UTXOs for known addresses
	// Note: Since we haven't done a full rescan, this may return empty
	// but the endpoint should work without errors
	body := fmt.Sprintf(`{"addresses": ["%s", "%s"]}`, satoshiAddress, halFinneyAddress)
	var resp UTXOsResponse
	if err := postJSON(t, client, baseURL, "/v1/utxos", authToken, body, &resp); err != nil {
		t.Fatalf("Failed to get UTXOs: %v", err)
	}

	t.Logf("UTXOs response: count=%d", len(resp.UTXOs))

	// The UTXOs list should be present (even if empty without rescan)
	if resp.UTXOs == nil {
		t.Error("UTXOs should not be nil")
	}
}
