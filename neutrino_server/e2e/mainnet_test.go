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
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	// Timeouts
	syncTimeout      = 15 * time.Minute // Max time to wait for initial sync
	syncPollInterval = 5 * time.Second
	requestTimeout   = 30 * time.Second
	startupTimeout   = 30 * time.Second

	// Minimum sync requirements
	minBlockHeight = 100000 // Wait until at least this height before running tests
	minPeers       = 1      // Need at least one peer
)

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

// FeeEstimateResponse represents the /v1/fees/estimate response
type FeeEstimateResponse struct {
	FeeRate      int `json:"fee_rate"`
	TargetBlocks int `json:"target_blocks"`
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

	// Get a random available port
	port, err := getFreePort()
	if err != nil {
		t.Fatalf("Failed to get free port: %v", err)
	}
	listenAddr := fmt.Sprintf("127.0.0.1:%d", port)
	baseURL := "http://" + listenAddr
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
			cmd.Process.Signal(syscall.SIGTERM)
			// Wait with timeout
			done := make(chan error, 1)
			go func() {
				done <- cmd.Wait()
			}()
			select {
			case <-done:
				t.Log("Server stopped gracefully")
			case <-time.After(5 * time.Second):
				t.Log("Server did not stop gracefully, killing...")
				cmd.Process.Kill()
				cmd.Wait()
			}
		}
	}()

	// Wait for server to be ready
	if err := waitForServer(t, baseURL); err != nil {
		t.Fatalf("Server failed to start: %v", err)
	}

	// Wait for initial sync
	if err := waitForSync(t, baseURL); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	// Run the actual tests
	t.Run("Status", func(t *testing.T) { testStatus(t, baseURL) })
	t.Run("Peers", func(t *testing.T) { testPeers(t, baseURL) })
	t.Run("GenesisBlock", func(t *testing.T) { testGenesisBlock(t, baseURL) })
	t.Run("Block100000", func(t *testing.T) { testBlock100000(t, baseURL) })
	t.Run("Block500000", func(t *testing.T) { testBlock500000(t, baseURL) })
	t.Run("FeeEstimate", func(t *testing.T) { testFeeEstimate(t, baseURL) })
	t.Run("WatchAddress", func(t *testing.T) { testWatchAddress(t, baseURL) })
	t.Run("UTXOs", func(t *testing.T) { testUTXOs(t, baseURL) })
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

	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}

	t.Logf("Server started with PID: %d", cmd.Process.Pid)
	return cmd
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

// waitForServer waits for the HTTP server to be ready
func waitForServer(t *testing.T, baseURL string) error {
	t.Helper()
	t.Log("Waiting for server to be ready...")

	deadline := time.Now().Add(startupTimeout)
	client := &http.Client{Timeout: 2 * time.Second}

	for time.Now().Before(deadline) {
		resp, err := client.Get(baseURL + "/v1/status")
		if err == nil {
			resp.Body.Close()
			t.Log("Server is ready")
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("server did not become ready within %v", startupTimeout)
}

// waitForSync waits for the node to sync to a minimum height
func waitForSync(t *testing.T, baseURL string) error {
	t.Helper()
	t.Logf("Waiting for sync to height %d...", minBlockHeight)

	deadline := time.Now().Add(syncTimeout)
	client := &http.Client{Timeout: requestTimeout}
	lastHeight := int32(0)

	for time.Now().Before(deadline) {
		resp, err := client.Get(baseURL + "/v1/status")
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
		if status.BlockHeight >= minBlockHeight && status.Peers >= minPeers {
			t.Logf("Sync complete: height=%d, peers=%d", status.BlockHeight, status.Peers)
			return nil
		}

		time.Sleep(syncPollInterval)
	}

	return fmt.Errorf("sync did not complete within %v (last height: %d)", syncTimeout, lastHeight)
}

// HTTP helpers

func getJSON(t *testing.T, baseURL, path string, result any) error {
	t.Helper()
	client := &http.Client{Timeout: requestTimeout}

	resp, err := client.Get(baseURL + path)
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

func postJSON(t *testing.T, baseURL, path string, body string, result any) error {
	t.Helper()
	client := &http.Client{Timeout: requestTimeout}

	resp, err := client.Post(baseURL+path, "application/json", strings.NewReader(body))
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

func testStatus(t *testing.T, baseURL string) {
	var status StatusResponse
	if err := getJSON(t, baseURL, "/v1/status", &status); err != nil {
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

func testPeers(t *testing.T, baseURL string) {
	var peers PeersResponse
	if err := getJSON(t, baseURL, "/v1/peers", &peers); err != nil {
		t.Fatalf("Failed to get peers: %v", err)
	}

	t.Logf("Peers: count=%d", peers.Count)

	if peers.Count < minPeers {
		t.Errorf("Peer count %d is less than expected minimum %d", peers.Count, minPeers)
	}
}

func testGenesisBlock(t *testing.T, baseURL string) {
	var header BlockHeaderResponse
	if err := getJSON(t, baseURL, "/v1/block/0/header", &header); err != nil {
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

func testBlock100000(t *testing.T, baseURL string) {
	var header BlockHeaderResponse
	if err := getJSON(t, baseURL, "/v1/block/100000/header", &header); err != nil {
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

func testBlock500000(t *testing.T, baseURL string) {
	// First check if we're synced high enough
	var status StatusResponse
	if err := getJSON(t, baseURL, "/v1/status", &status); err != nil {
		t.Fatalf("Failed to get status: %v", err)
	}

	if status.BlockHeight < 500000 {
		t.Skipf("Skipping block 500000 test - current height %d is too low", status.BlockHeight)
	}

	var header BlockHeaderResponse
	if err := getJSON(t, baseURL, "/v1/block/500000/header", &header); err != nil {
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

func testFeeEstimate(t *testing.T, baseURL string) {
	var fee FeeEstimateResponse
	if err := getJSON(t, baseURL, "/v1/fees/estimate?target_blocks=6", &fee); err != nil {
		t.Fatalf("Failed to get fee estimate: %v", err)
	}

	t.Logf("Fee estimate: fee_rate=%d sat/vB, target_blocks=%d", fee.FeeRate, fee.TargetBlocks)

	// Fee rate should be positive
	if fee.FeeRate <= 0 {
		t.Errorf("Fee rate should be positive, got %d", fee.FeeRate)
	}

	// Target blocks should match our request
	if fee.TargetBlocks != 6 {
		t.Errorf("Target blocks should be 6, got %d", fee.TargetBlocks)
	}
}

func testWatchAddress(t *testing.T, baseURL string) {
	// Watch Satoshi's address
	body := fmt.Sprintf(`{"address": "%s"}`, satoshiAddress)
	var resp WatchResponse
	if err := postJSON(t, baseURL, "/v1/watch/address", body, &resp); err != nil {
		t.Fatalf("Failed to watch address: %v", err)
	}

	t.Logf("Watch address response: status=%s", resp.Status)

	if resp.Status != "ok" {
		t.Errorf("Expected status 'ok', got '%s'", resp.Status)
	}
}

func testUTXOs(t *testing.T, baseURL string) {
	// Query UTXOs for known addresses
	// Note: Since we haven't done a full rescan, this may return empty
	// but the endpoint should work without errors
	body := fmt.Sprintf(`{"addresses": ["%s", "%s"]}`, satoshiAddress, halFinneyAddress)
	var resp UTXOsResponse
	if err := postJSON(t, baseURL, "/v1/utxos", body, &resp); err != nil {
		t.Fatalf("Failed to get UTXOs: %v", err)
	}

	t.Logf("UTXOs response: count=%d", len(resp.UTXOs))

	// The UTXOs list should be present (even if empty without rescan)
	if resp.UTXOs == nil {
		t.Error("UTXOs should not be nil")
	}
}
