/*
Package neutrino provides a wrapper around the lightninglabs/neutrino library.

This package initializes and manages a neutrino light client node that uses
BIP157/BIP158 compact block filters for privacy-preserving blockchain access.
*/
package neutrino

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/btcutil/gcs/builder"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/connmgr"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btclog"
	"github.com/btcsuite/btcwallet/walletdb"
	_ "github.com/btcsuite/btcwallet/walletdb/bdb" // Import bbolt driver
	"github.com/lightninglabs/neutrino"
	"golang.org/x/net/proxy"
)

// Config holds configuration for the neutrino node.
type Config struct {
	Network             string
	DataDir             string
	TorProxy            string
	AddPeers            string
	MaxPeers            int
	BanDuration         time.Duration
	FilterCacheSize     int
	PrefetchFilters     bool
	PrefetchWorkers     int
	PrefetchStart       int32
	PrefetchLookback    int32 // >0: auto-compute start as tip-lookback when PrefetchStart==0
	ClearnetInitialSync bool  // When true and TorProxy set, sync headers over clearnet first
	Logger              *btclog.Backend
	LogLevel            string
}

// Node wraps a neutrino ChainService with additional functionality.
type Node struct {
	config       *Config
	chainParams  *chaincfg.Params
	chainService *neutrino.ChainService
	rescanMgr    *RescanManager
	logger       btclog.Logger
	db           walletdb.DB

	mu           sync.RWMutex
	synced       bool
	blockHeight  int32
	filterHeight int32

	prefetchMu         sync.RWMutex
	prefetchInProgress bool
	prefetchLastHeight int32
	prefetchQuit       chan struct{}
	prefetchDone       chan struct{}
}

// UTXO represents an unspent transaction output.
type UTXO struct {
	TxID         string `json:"txid"`
	Vout         uint32 `json:"vout"`
	Value        int64  `json:"value"`
	Address      string `json:"address"`
	ScriptPubKey string `json:"scriptpubkey"`
	Height       int32  `json:"height"`
}

// Transaction represents a blockchain transaction.
type Transaction struct {
	TxID        string `json:"txid"`
	Hex         string `json:"hex"`
	BlockHeight int32  `json:"block_height,omitempty"`
	BlockTime   int64  `json:"block_time,omitempty"`
}

// Status represents the current node status.
type Status struct {
	Synced       bool  `json:"synced"`
	BlockHeight  int32 `json:"block_height"`
	FilterHeight int32 `json:"filter_height"`
	Peers        int   `json:"peers"`
}

// NewNode creates a new neutrino node.
func NewNode(config *Config) (*Node, error) {
	if config == nil {
		return nil, errors.New("config is required")
	}

	chainParams, err := getChainParams(config.Network)
	if err != nil {
		return nil, fmt.Errorf("invalid network %s: %w", config.Network, err)
	}

	logger := config.Logger.Logger("NTRN")
	// Use the configured log level
	logLevel := config.LogLevel
	if logLevel == "" {
		logLevel = "info"
	}
	level, _ := btclog.LevelFromString(logLevel)
	logger.SetLevel(level)

	logger.Infof("Initializing neutrino node for network: %s", config.Network)
	logger.Infof("Data directory: %s", config.DataDir)
	logger.Infof("Log level: %s", logLevel)
	if config.AddPeers != "" {
		logger.Infof("Preferred peers: %s", config.AddPeers)
	}

	node := &Node{
		config:       config,
		chainParams:  chainParams,
		logger:       logger,
		prefetchQuit: make(chan struct{}),
		prefetchDone: make(chan struct{}),
	}
	node.prefetchLastHeight = config.PrefetchStart - 1

	return node, nil
}

// Start initializes and starts the neutrino node.
// When ClearnetInitialSync is enabled and a TorProxy is configured, the node
// performs a two-phase startup:
//  1. Phase 1 (clearnet): Sync block headers and filter headers without Tor.
//     This is safe because headers are deterministic public data identical for
//     all nodes -- downloading them reveals nothing about watched addresses.
//  2. Phase 2 (Tor): Stop the clearnet chain service and restart with Tor for
//     all subsequent operations (filter fetches, block downloads, broadcasts).
func (n *Node) Start() error {
	n.logger.Info("Starting neutrino node...")

	// Open the database for neutrino
	dbPath := filepath.Join(n.config.DataDir, "neutrino.db")
	n.logger.Infof("Opening database at: %s", dbPath)
	db, err := walletdb.Create("bdb", dbPath, true, 60*time.Second)
	if err != nil {
		return fmt.Errorf("failed to create database at %s: %w", dbPath, err)
	}
	n.db = db

	// Configure logging for the neutrino library itself
	logLevel := n.config.LogLevel
	if logLevel == "" {
		logLevel = "info"
	}
	level, _ := btclog.LevelFromString(logLevel)

	// Set log level for neutrino library's internal loggers
	neutrinoLogger := n.config.Logger.Logger("NTRNO")
	neutrinoLogger.SetLevel(level)
	neutrino.UseLogger(neutrinoLogger)

	// Two-phase startup: clearnet headers sync, then switch to Tor
	if n.config.ClearnetInitialSync && n.config.TorProxy != "" {
		n.logger.Info("=== Phase 1: Syncing headers over clearnet (fast) ===")
		n.logger.Info("Block headers and filter headers are public deterministic data,")
		n.logger.Info("downloading them over clearnet does not reveal watched addresses.")

		if err := n.startChainService(false); err != nil {
			n.db.Close()
			return fmt.Errorf("clearnet sync failed: %w", err)
		}

		// Wait for header sync to complete
		n.logger.Info("Waiting for block header and filter header sync to complete...")
		if err := n.waitForHeaderSync(); err != nil {
			// Non-fatal: fall through to Tor startup with partial headers
			n.logger.Warnf("Clearnet header sync did not complete: %v", err)
			n.logger.Info("Proceeding to Tor mode with partial headers (will catch up via Tor)")
		} else {
			n.logger.Info("Header sync complete over clearnet")
		}

		// Stop the clearnet chain service (DB stays open)
		n.logger.Info("=== Phase 2: Switching to Tor for privacy-sensitive operations ===")
		if err := n.chainService.Stop(); err != nil {
			n.logger.Warnf("Failed to stop clearnet chain service: %v", err)
		}
		n.chainService = nil

		// Start with Tor
		if err := n.startChainService(true); err != nil {
			n.db.Close()
			return fmt.Errorf("tor chain service start failed: %w", err)
		}
		n.logger.Info("Chain service restarted with Tor proxy")
	} else {
		// Single-phase startup (no Tor, or clearnet-initial-sync disabled)
		useTor := n.config.TorProxy != ""
		if err := n.startChainService(useTor); err != nil {
			n.db.Close()
			return err
		}
	}

	// Open rescan state store for persistence
	stateStore, err := OpenStateStore(n.config.DataDir, n.logger)
	if err != nil {
		n.logger.Warnf("Failed to open rescan state store (persistence disabled): %v", err)
		// Non-fatal: fall back to in-memory only mode.
		stateStore = nil
	}

	// Create rescan manager with persistence
	n.rescanMgr = NewRescanManager(n.chainService, n.logger, stateStore)

	// Start sync monitoring goroutine
	go n.monitorSync()

	if n.config.PrefetchFilters {
		go n.prefetchFilters()
	}

	n.logger.Info("Neutrino node started")
	return nil
}

// startChainService creates and starts a neutrino.ChainService.
// When useTor is true, all connections are routed through the configured Tor
// SOCKS5 proxy. The method reuses the already-open n.db database.
func (n *Node) startChainService(useTor bool) error {
	// Create neutrino config
	neutrinoConfig := neutrino.Config{
		DataDir:         n.config.DataDir,
		Database:        n.db,
		ChainParams:     *n.chainParams,
		FilterCacheSize: uint64(n.config.FilterCacheSize),
		PersistToDisk:   true,
	}

	// Add preferred peers if specified (discovery remains enabled).
	if n.config.AddPeers != "" {
		peers := strings.Split(n.config.AddPeers, ",")
		for _, peer := range peers {
			peer = strings.TrimSpace(peer)
			if peer != "" {
				n.logger.Infof("Adding preferred peer: %s", peer)
				neutrinoConfig.AddPeers = append(neutrinoConfig.AddPeers, peer)
			}
		}
		n.logger.Infof("Total add peers configured: %d", len(neutrinoConfig.AddPeers))
	}

	// Always add DNS seeds to preserve discovery and broaden filter-serving peer set.
	seeds := getDNSSeeds(n.config.Network)
	if len(seeds) > 0 {
		neutrinoConfig.AddPeers = append(neutrinoConfig.AddPeers, seeds...)
		n.logger.Infof("Using %d DNS seeds for discovery", len(seeds))
	}

	// Configure Tor proxy if requested
	if useTor && n.config.TorProxy != "" {
		n.logger.Infof("Configuring Tor SOCKS5 proxy: %s", n.config.TorProxy)

		// Create a SOCKS5 dialer
		torDialer, err := proxy.SOCKS5("tcp", n.config.TorProxy, nil, proxy.Direct)
		if err != nil {
			return fmt.Errorf("failed to create Tor SOCKS5 dialer: %w", err)
		}

		// Set up DNS resolution through Tor to prevent DNS leaks
		neutrinoConfig.NameResolver = func(host string) ([]net.IP, error) {
			if ip := net.ParseIP(host); ip != nil {
				return []net.IP{ip}, nil
			}

			if strings.HasSuffix(host, ".onion") {
				return []net.IP{net.IP([]byte(host))}, nil
			}

			ips, err := connmgr.TorLookupIP(host, n.config.TorProxy)
			if err != nil {
				n.logger.Warnf("Tor DNS lookup failed for %s: %v", host, err)
				return nil, err
			}

			return ips, nil
		}

		// Set up the custom dialer that routes through Tor
		neutrinoConfig.Dialer = func(addr net.Addr) (net.Conn, error) {
			targetAddr := addr.String()

			if tcpAddr, ok := addr.(*net.TCPAddr); ok && len(tcpAddr.IP) > 16 {
				hostname := string(tcpAddr.IP)
				targetAddr = net.JoinHostPort(hostname, fmt.Sprintf("%d", tcpAddr.Port))
			}

			return torDialer.Dial("tcp", targetAddr)
		}

		n.logger.Info("Tor proxy configured successfully (DNS resolution via Tor)")
	} else if useTor {
		n.logger.Warn("Tor requested but no proxy configured, using clearnet")
	}

	modeStr := "clearnet"
	if useTor && n.config.TorProxy != "" {
		modeStr = "Tor"
	}
	n.logger.Infof("Creating chain service for network %s (%s mode)", n.chainParams.Name, modeStr)

	// Create chain service
	chainService, err := neutrino.NewChainService(neutrinoConfig)
	if err != nil {
		return fmt.Errorf("failed to create chain service: %w", err)
	}

	n.chainService = chainService
	n.logger.Info("Chain service created successfully")

	// Start the chain service
	n.logger.Info("Starting chain service...")
	if err := n.chainService.Start(); err != nil {
		return fmt.Errorf("failed to start chain service: %w", err)
	}
	n.logger.Infof("Chain service started successfully (%s)", modeStr)

	return nil
}

// waitForHeaderSync blocks until the chain service reports IsCurrent() or a
// timeout is reached. Uses a generous timeout since mainnet header sync over
// clearnet typically takes 2-10 minutes.
func (n *Node) waitForHeaderSync() error {
	const (
		timeout      = 30 * time.Minute
		pollInterval = 5 * time.Second
		logInterval  = 30 * time.Second
	)

	start := time.Now()
	lastLog := start
	lastHeight := int32(-1)

	for {
		if n.chainService == nil {
			return errors.New("chain service is nil")
		}

		if n.chainService.IsCurrent() {
			bestBlock, err := n.chainService.BestBlock()
			if err == nil {
				elapsed := time.Since(start)
				n.logger.Infof("Header sync complete: height %d in %s", bestBlock.Height, elapsed.Round(time.Second))
			}
			return nil
		}

		// Log progress
		now := time.Now()
		if now.Sub(lastLog) >= logInterval {
			bestBlock, _ := n.chainService.BestBlock()
			height := int32(0)
			if bestBlock != nil {
				height = bestBlock.Height
			}
			peers := len(n.chainService.Peers())
			elapsed := now.Sub(start)

			if height > lastHeight && lastHeight >= 0 {
				blocksPerSec := float64(height-lastHeight) / logInterval.Seconds()
				n.logger.Infof("Header sync: height %d, peers %d, %.0f headers/sec (%s elapsed)",
					height, peers, blocksPerSec, elapsed.Round(time.Second))
			} else {
				n.logger.Infof("Header sync: height %d, peers %d (%s elapsed)",
					height, peers, elapsed.Round(time.Second))
			}
			lastHeight = height
			lastLog = now
		}

		if time.Since(start) >= timeout {
			return fmt.Errorf("header sync did not complete within %s", timeout)
		}

		time.Sleep(pollInterval)
	}
}

// Stop gracefully stops the neutrino node.
func (n *Node) Stop() error {
	n.logger.Info("Stopping neutrino node...")

	// Close rescan manager first (persists final state and closes state DB).
	if n.rescanMgr != nil {
		if err := n.rescanMgr.Close(); err != nil {
			n.logger.Warnf("Failed to close rescan manager: %v", err)
		}
	}

	if n.config.PrefetchFilters {
		close(n.prefetchQuit)
		<-n.prefetchDone
	}

	if n.chainService != nil {
		if err := n.chainService.Stop(); err != nil {
			return fmt.Errorf("failed to stop chain service: %w", err)
		}
	}

	if n.db != nil {
		if err := n.db.Close(); err != nil {
			return fmt.Errorf("failed to close database: %w", err)
		}
	}

	n.logger.Info("Neutrino node stopped")
	return nil
}

// GetStatus returns the current node status.
func (n *Node) GetStatus() Status {
	n.mu.RLock()
	defer n.mu.RUnlock()

	peers := 0
	if n.chainService != nil {
		peers = len(n.chainService.Peers())
	}

	return Status{
		Synced:       n.synced,
		BlockHeight:  n.blockHeight,
		FilterHeight: n.filterHeight,
		Peers:        peers,
	}
}

// GetBlockHeight returns the current block height.
func (n *Node) GetBlockHeight() int32 {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.blockHeight
}

// GetBlockHeader returns the block header at the given height.
func (n *Node) GetBlockHeader(height int32) (*wire.BlockHeader, error) {
	if n.chainService == nil {
		return nil, errors.New("chain service not initialized")
	}

	blockHash, err := n.chainService.GetBlockHash(int64(height))
	if err != nil {
		return nil, fmt.Errorf("failed to get block hash: %w", err)
	}

	header, err := n.chainService.GetBlockHeader(blockHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get block header: %w", err)
	}

	return header, nil
}

// GetBlockHash returns the block hash at the given height.
func (n *Node) GetBlockHash(height int32) (*chainhash.Hash, error) {
	if n.chainService == nil {
		return nil, errors.New("chain service not initialized")
	}

	return n.chainService.GetBlockHash(int64(height))
}

// BroadcastTransaction broadcasts a transaction to the network.
func (n *Node) BroadcastTransaction(tx *wire.MsgTx) error {
	if n.chainService == nil {
		return errors.New("chain service not initialized")
	}

	// Use the pushtx package to broadcast
	return n.chainService.SendTransaction(tx)
}

// GetUTXOs scans for UTXOs belonging to the given addresses.
func (n *Node) GetUTXOs(addresses []string) ([]UTXO, error) {
	if n.rescanMgr == nil {
		return nil, errors.New("rescan manager not initialized")
	}

	return n.rescanMgr.GetUTXOs(addresses)
}

// WatchAddress adds an address to the watch list.
func (n *Node) WatchAddress(address string) error {
	if n.rescanMgr == nil {
		return errors.New("rescan manager not initialized")
	}

	return n.rescanMgr.WatchAddress(address)
}

// Rescan triggers a rescan from the given height.
func (n *Node) Rescan(startHeight int32, addresses []string) error {
	if n.rescanMgr == nil {
		return errors.New("rescan manager not initialized")
	}

	return n.rescanMgr.Rescan(startHeight, addresses)
}

// IsRescanInProgress returns true if a rescan is currently running.
func (n *Node) IsRescanInProgress() bool {
	if n.rescanMgr == nil {
		return false
	}
	return n.rescanMgr.IsRescanInProgress()
}

// RescanStatus returns detailed rescan lifecycle status.
func (n *Node) RescanStatus() RescanStatus {
	if n.rescanMgr == nil {
		return RescanStatus{}
	}
	return n.rescanMgr.GetRescanStatus()
}

// UTXOSpendReport represents information about a UTXO.
type UTXOSpendReport struct {
	// If the output is unspent, these fields are populated
	Unspent      bool   `json:"unspent"`
	Value        int64  `json:"value,omitempty"`
	ScriptPubKey string `json:"scriptpubkey,omitempty"`
	BlockHeight  uint32 `json:"block_height,omitempty"`

	// If the output has been spent, these fields are populated
	SpendingTxID   string `json:"spending_txid,omitempty"`
	SpendingInput  uint32 `json:"spending_input,omitempty"`
	SpendingHeight uint32 `json:"spending_height,omitempty"`
}

// GetUTXO checks if a UTXO exists and whether it has been spent.
// It scans from startHeight forward to the chain tip, looking for the UTXO creation
// and any subsequent spend.
//
// IMPORTANT: address is REQUIRED because neutrino uses compact block filters (BIP158)
// which match on scriptPubKeys, not outpoints. Without the address/script, we cannot
// find the UTXO in the filters.
//
// startHeight should be set to the block height where the UTXO was created (or slightly before).
// This is critical for performance - scanning from genesis is very slow.
func (n *Node) GetUTXO(txid string, vout uint32, address string, startHeight int32) (*UTXOSpendReport, error) {
	if n.chainService == nil {
		return nil, errors.New("chain service not initialized")
	}

	if address == "" {
		return nil, NewBadRequestError("address is required: neutrino uses compact block filters which match on scripts, not outpoints")
	}

	// Parse the address to get the pkScript
	addr, err := btcutil.DecodeAddress(address, n.chainParams)
	if err != nil {
		return nil, NewBadRequestError(fmt.Sprintf("invalid address %s: %v", address, err))
	}

	pkScript, err := txscript.PayToAddrScript(addr)
	if err != nil {
		return nil, fmt.Errorf("failed to create script for address %s: %w", address, err)
	}

	// Parse txid
	targetHash, err := chainhash.NewHashFromStr(txid)
	if err != nil {
		return nil, NewBadRequestError(fmt.Sprintf("invalid txid: %v", err))
	}

	n.logger.Infof("Looking up UTXO %s:%d for address %s starting from height %d", txid, vout, address, startHeight)

	// Get current best block
	bestBlock, err := n.chainService.BestBlock()
	if err != nil {
		return nil, fmt.Errorf("failed to get best block: %w", err)
	}

	endHeight := bestBlock.Height
	n.logger.Debugf("Scanning from height %d to %d", startHeight, endHeight)

	// Scan blocks to find the transaction and any spend
	var foundTx *wire.MsgTx
	var foundHeight int32
	var spendingTxHash string
	var spendingInputIdx uint32
	var spendingHeight int32

	for height := startHeight; height <= endHeight; height++ {
		// Get block hash
		blockHash, err := n.chainService.GetBlockHash(int64(height))
		if err != nil {
			n.logger.Debugf("Failed to get block hash for height %d: %v", height, err)
			continue
		}

		// Get compact block filter
		filter, err := n.chainService.GetCFilter(*blockHash, wire.GCSFilterRegular)
		if err != nil {
			n.logger.Debugf("Failed to get filter for block %d: %v", height, err)
			continue
		}

		if filter == nil {
			continue
		}

		// Check if the filter matches our pkScript
		key := builder.DeriveKey(blockHash)
		matched, err := filter.Match(key, pkScript)
		if err != nil {
			n.logger.Debugf("Filter match error for block %d: %v", height, err)
			continue
		}

		if !matched {
			continue
		}

		n.logger.Debugf("Block %d filter matched, fetching full block", height)

		// Filter matched - fetch the full block
		block, err := n.chainService.GetBlock(*blockHash)
		if err != nil {
			n.logger.Warnf("Failed to get block %d: %v", height, err)
			continue
		}

		// Scan all transactions in the block
		for _, tx := range block.Transactions() {
			txHash := tx.Hash()

			// Check if this is the transaction we're looking for
			if foundTx == nil && txHash.IsEqual(targetHash) {
				// Found the transaction creating the UTXO
				if int(vout) < len(tx.MsgTx().TxOut) {
					foundTx = tx.MsgTx()
					foundHeight = height
					n.logger.Infof("Found UTXO creation at height %d", height)
				}
			}

			// Check if this transaction spends our UTXO
			if foundTx != nil {
				for inputIdx, txIn := range tx.MsgTx().TxIn {
					prevOut := txIn.PreviousOutPoint
					if prevOut.Hash.IsEqual(targetHash) && prevOut.Index == vout {
						// Found the spending transaction
						spendingTxHash = txHash.String()
						spendingInputIdx = uint32(inputIdx)
						spendingHeight = height
						n.logger.Infof("Found UTXO spend at height %d in tx %s", height, spendingTxHash)
						break
					}
				}
			}
		}

		// If we found both creation and spend, we can stop
		if foundTx != nil && spendingTxHash != "" {
			break
		}
	}

	// Build response
	if foundTx == nil {
		return nil, NewNotFoundError("UTXO", "UTXO not found: ensure start_height is at or before the block containing the transaction")
	}

	report := &UTXOSpendReport{}

	if spendingTxHash == "" {
		// UTXO is unspent
		report.Unspent = true
		txOut := foundTx.TxOut[vout]
		report.Value = txOut.Value
		report.ScriptPubKey = fmt.Sprintf("%x", txOut.PkScript)
		report.BlockHeight = uint32(foundHeight)
	} else {
		// UTXO has been spent
		report.Unspent = false
		report.SpendingTxID = spendingTxHash
		report.SpendingInput = spendingInputIdx
		report.SpendingHeight = uint32(spendingHeight)
	}

	n.logger.Infof("UTXO %s:%d found at height %d, unspent=%v", txid, vout, foundHeight, report.Unspent)
	return report, nil
}

// monitorSync monitors the sync status and updates internal state.
func (n *Node) monitorSync() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	lastPeerCount := -1
	lastHeight := int32(-1)

	for range ticker.C {
		if n.chainService == nil {
			continue
		}

		// Get peer count
		peers := n.chainService.Peers()
		peerCount := len(peers)

		// Log peer changes
		if peerCount != lastPeerCount {
			if peerCount == 0 {
				n.logger.Warn("No peers connected - waiting for peer connections...")
			} else {
				n.logger.Infof("Peer count changed: %d -> %d", lastPeerCount, peerCount)
				for i, peer := range peers {
					n.logger.Debugf("  Peer %d: %s", i, peer.Addr())
				}
			}
			lastPeerCount = peerCount
		}

		// Get best block
		bestBlock, err := n.chainService.BestBlock()
		if err != nil {
			n.logger.Warnf("Failed to get best block: %v", err)
			continue
		}

		// Log height changes
		if bestBlock.Height != lastHeight {
			n.logger.Infof("Block height: %d (was %d)", bestBlock.Height, lastHeight)
			lastHeight = bestBlock.Height
		}

		// Use IsCurrent() as the primary sync indicator
		// The neutrino library tracks filter sync internally
		isCurrent := n.chainService.IsCurrent()

		n.mu.Lock()
		wasSynced := n.synced
		n.blockHeight = bestBlock.Height
		n.filterHeight = bestBlock.Height // Assume filters are synced when blocks are synced
		n.synced = isCurrent
		n.mu.Unlock()

		// Log sync status changes
		if isCurrent && !wasSynced {
			n.logger.Infof("Sync complete! Block height: %d, Peers: %d", bestBlock.Height, peerCount)
		} else if !isCurrent {
			n.logger.Debugf("Syncing... blocks: %d, peers: %d, isCurrent: %v", bestBlock.Height, peerCount, isCurrent)
		}
	}
}

func computePrefetchWorkerCount(configured int) int {
	if configured > 0 {
		if configured > 32 {
			return 32
		}
		return configured
	}

	workers := runtime.GOMAXPROCS(0)
	if workers < 4 {
		workers = 4
	}
	if workers > 16 {
		workers = 16
	}
	return workers
}

func (n *Node) prefetchFilters() {
	defer close(n.prefetchDone)

	workers := computePrefetchWorkerCount(n.config.PrefetchWorkers)
	n.logger.Infof("Background filter prefetch enabled: workers=%d, start_height=%d, lookback=%d",
		workers, n.config.PrefetchStart, n.config.PrefetchLookback)

	for {
		select {
		case <-n.prefetchQuit:
			n.logger.Info("Background filter prefetch stopped")
			return
		default:
		}

		if n.chainService == nil {
			if !n.prefetchSleep(2 * time.Second) {
				return
			}
			continue
		}

		// Wait for initial header/filter-header sync.
		if !n.chainService.IsCurrent() {
			if !n.prefetchSleep(5 * time.Second) {
				return
			}
			continue
		}

		// Auto-compute prefetch start from lookback if not explicitly set.
		// This runs once after initial sync when we know the chain tip.
		n.prefetchMu.Lock()
		if n.config.PrefetchStart == 0 && n.config.PrefetchLookback > 0 && n.prefetchLastHeight < 0 {
			bestBlock, err := n.chainService.BestBlock()
			if err == nil && bestBlock.Height > n.config.PrefetchLookback {
				computedStart := bestBlock.Height - n.config.PrefetchLookback
				n.prefetchLastHeight = computedStart - 1
				n.logger.Infof("Prefetch: auto-computed start height %d (tip %d - lookback %d)",
					computedStart, bestBlock.Height, n.config.PrefetchLookback)
			}
		}
		n.prefetchMu.Unlock()

		n.runPrefetchPass(workers)

		// Keep running in the background to fetch filters for newly arrived blocks.
		if !n.prefetchSleep(15 * time.Second) {
			return
		}
	}
}

func (n *Node) prefetchSleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-n.prefetchQuit:
		return false
	case <-t.C:
		return true
	}
}

func (n *Node) runPrefetchPass(workers int) {
	bestBlock, err := n.chainService.BestBlock()
	if err != nil {
		n.logger.Warnf("Prefetch: failed to get best block: %v", err)
		return
	}

	n.prefetchMu.RLock()
	start := n.prefetchLastHeight + 1
	inProgress := n.prefetchInProgress
	n.prefetchMu.RUnlock()

	if inProgress {
		return
	}

	if start < n.config.PrefetchStart {
		start = n.config.PrefetchStart
	}

	if start > bestBlock.Height {
		return
	}

	total := bestBlock.Height - start + 1
	n.logger.Infof("Prefetch pass: heights %d..%d (%d blocks)", start, bestBlock.Height, total)

	n.prefetchMu.Lock()
	n.prefetchInProgress = true
	n.prefetchMu.Unlock()
	defer func() {
		n.prefetchMu.Lock()
		n.prefetchInProgress = false
		n.prefetchMu.Unlock()
	}()

	type prefetchResult struct {
		height int32
		ok     bool
	}

	heightJobs := make(chan int32, workers*4)
	results := make(chan prefetchResult, workers*4)

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for height := range heightJobs {
				blockHash, err := n.chainService.GetBlockHash(int64(height))
				if err != nil {
					n.logger.Debugf("Prefetch: failed block hash at %d: %v", height, err)
					results <- prefetchResult{height: height, ok: false}
					continue
				}

				_, err = n.chainService.GetCFilter(*blockHash, wire.GCSFilterRegular)
				if err != nil {
					n.logger.Debugf("Prefetch: failed cfilter at %d: %v", height, err)
					results <- prefetchResult{height: height, ok: false}
					continue
				}

				results <- prefetchResult{height: height, ok: true}
			}
		}()
	}

	go func() {
		for height := start; height <= bestBlock.Height; height++ {
			select {
			case <-n.prefetchQuit:
				close(heightJobs)
				wg.Wait()
				close(results)
				return
			case heightJobs <- height:
			}
		}
		close(heightJobs)
		wg.Wait()
		close(results)
	}()

	started := time.Now()
	lastLog := started
	scanned := int32(0)
	succeeded := int32(0)
	maxSeen := start - 1

	for res := range results {
		scanned++
		if res.ok {
			succeeded++
		}
		if res.height > maxSeen {
			maxSeen = res.height
		}

		now := time.Now()
		if now.Sub(lastLog) >= 10*time.Second {
			elapsed := now.Sub(started)
			bps := float64(scanned) / elapsed.Seconds()
			remaining := int32(0)
			if bps > 0 {
				remaining = int32(float64(total-scanned) / bps)
			}
			pct := float64(scanned) / float64(total) * 100
			n.logger.Infof("Prefetch progress: %d/%d (%.1f%%) | height %d | %.1f blocks/sec | ~%ds remaining",
				scanned, total, pct, maxSeen, bps, remaining)
			lastLog = now
		}
	}

	n.prefetchMu.Lock()
	if maxSeen > n.prefetchLastHeight {
		n.prefetchLastHeight = maxSeen
	}
	n.prefetchMu.Unlock()

	elapsed := time.Since(started)
	bps := float64(0)
	if elapsed.Seconds() > 0 {
		bps = float64(scanned) / elapsed.Seconds()
	}
	n.logger.Infof("Prefetch complete: %d/%d filters fetched in %s (%.1f blocks/sec), tip=%d",
		succeeded, scanned, elapsed.Round(time.Millisecond), bps, bestBlock.Height)
}

// getChainParams returns the chain parameters for the given network.
func getChainParams(network string) (*chaincfg.Params, error) {
	switch network {
	case "mainnet":
		return &chaincfg.MainNetParams, nil
	case "testnet":
		return &chaincfg.TestNet3Params, nil
	case "regtest":
		return &chaincfg.RegressionNetParams, nil
	case "signet":
		return &chaincfg.SigNetParams, nil
	default:
		return nil, fmt.Errorf("unknown network: %s", network)
	}
}

// getDNSSeeds returns DNS seeds for the given network.
func getDNSSeeds(network string) []string {
	switch network {
	case "mainnet":
		return []string{
			"seed.bitcoin.sipa.be",
			"dnsseed.bluematt.me",
			"dnsseed.bitcoin.dashjr.org",
			"seed.bitcoinstats.com",
			"seed.bitcoin.jonasschnelli.ch",
		}
	case "testnet":
		return []string{
			"testnet-seed.bitcoin.jonasschnelli.ch",
			"seed.tbtc.petertodd.net",
			"testnet-seed.bluematt.me",
		}
	case "signet":
		return []string{
			"seed.signet.bitcoin.sprovoost.nl",
		}
	default:
		return []string{}
	}
}
