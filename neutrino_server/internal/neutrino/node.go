/*
Package neutrino provides a wrapper around the lightninglabs/neutrino library.

This package initializes and manages a neutrino light client node that uses
BIP157/BIP158 compact block filters for privacy-preserving blockchain access.
*/
package neutrino

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
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
	Version             string
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

	// CFilterCDNAuto enables automatic compact filter (cfilter) bulk
	// download from block-dn after P2P header sync completes. Filters
	// are verified against P2P-synced filter headers before storage.
	CFilterCDNAuto bool

	// CFilterCDNURL overrides the auto-resolved block-dn base URL for
	// compact filter downloads. When set, CFilterCDNAuto is ignored.
	CFilterCDNURL string
}

// Node wraps a neutrino ChainService with additional functionality.
type Node struct {
	config       *Config
	version      string
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
	Synced           bool   `json:"synced"`
	BlockHeight      int32  `json:"block_height"`
	FilterHeight     int32  `json:"filter_height"`
	Peers            int    `json:"peers"`
	Version          string `json:"version"`
	WatchedAddresses int    `json:"watched_addresses"`
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
		version:      strings.TrimSpace(config.Version),
		chainParams:  chainParams,
		logger:       logger,
		prefetchQuit: make(chan struct{}),
		prefetchDone: make(chan struct{}),
	}
	if node.version == "" {
		node.version = "dev"
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
			lookupHost := host
			if splitHost, _, err := net.SplitHostPort(host); err == nil {
				lookupHost = splitHost
			}

			if ip := net.ParseIP(lookupHost); ip != nil {
				return []net.IP{ip}, nil
			}

			if strings.HasSuffix(strings.ToLower(lookupHost), ".onion") {
				// Tor hostnames are not DNS-resolved. They are dialed directly
				// through SOCKS when selected as peers.
				return nil, nil
			}

			ips, err := connmgr.TorLookupIP(lookupHost, n.config.TorProxy)
			if err != nil {
				n.logger.Warnf("Tor DNS lookup failed for %s: %v", lookupHost, err)
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
	if err := n.chainService.Start(context.Background()); err != nil {
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

	watchedAddresses := 0
	if n.rescanMgr != nil {
		watchedAddresses = n.rescanMgr.WatchedAddressCount()
	}

	return Status{
		Synced:           n.synced,
		BlockHeight:      n.blockHeight,
		FilterHeight:     n.filterHeight,
		Peers:            peers,
		Version:          n.version,
		WatchedAddresses: watchedAddresses,
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

	// Phase 1: Try CDN bulk download if configured.
	useTor := n.config.TorProxy != ""
	cdnBaseURL := n.resolveCFilterCDNBaseURL()
	if cdnBaseURL != "" {
		cdnImported, cdnLastHeight, cdnErr := n.cdnPrefetchFilters(start, useTor)
		if cdnErr != nil {
			n.logger.Warnf("CDN cfilter prefetch error (will fall back to P2P): %v", cdnErr)
		}
		if cdnImported > 0 {
			n.logger.Infof("CDN cfilter prefetch imported %d filters up to height %d",
				cdnImported, cdnLastHeight)
		}
		if cdnLastHeight >= start {
			n.prefetchMu.Lock()
			if cdnLastHeight > n.prefetchLastHeight {
				n.prefetchLastHeight = cdnLastHeight
			}
			n.prefetchMu.Unlock()
			start = cdnLastHeight + 1
		}
	}

	// Phase 2: P2P prefetch for remaining blocks.
	if start > bestBlock.Height {
		n.logger.Infof("Prefetch complete: all filters obtained via CDN, tip=%d", bestBlock.Height)
		return
	}

	p2pTotal := bestBlock.Height - start + 1
	n.logger.Infof("P2P prefetch: heights %d..%d (%d blocks)", start, bestBlock.Height, p2pTotal)

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
				remaining = int32(float64(p2pTotal-scanned) / bps)
			}
			pct := float64(scanned) / float64(p2pTotal) * 100
			n.logger.Infof("P2P prefetch progress: %d/%d (%.1f%%) | height %d | %.1f blocks/sec | ~%ds remaining",
				scanned, p2pTotal, pct, maxSeen, bps, remaining)
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
	n.logger.Infof("P2P prefetch complete: %d/%d filters fetched in %s (%.1f blocks/sec), tip=%d",
		succeeded, scanned, elapsed.Round(time.Millisecond), bps, bestBlock.Height)
}

const (
	defaultBlockDNStatusTimeout   = 8 * time.Second
	defaultBlockDNFilterChunkSize = 2000
)

// blockDNStatus represents the /status response from a block-dn CDN server.
type blockDNStatus struct {
	ChainName            string `json:"chain_name"`
	BestBlockHeight      int32  `json:"best_block_height"`
	BestFilterHeight     int32  `json:"best_filter_height"`
	EntriesPerFilterFile int32  `json:"entries_per_filter_file"`
}

// resolveCFilterCDNBaseURL returns the base URL for cfilter downloads.
// If CFilterCDNURL is set explicitly, it is returned directly.
// If CFilterCDNAuto is enabled, the default block-dn URL for the network is
// returned. Otherwise, an empty string is returned (CDN disabled).
func (n *Node) resolveCFilterCDNBaseURL() string {
	if url := strings.TrimSpace(n.config.CFilterCDNURL); url != "" {
		return strings.TrimRight(url, "/")
	}
	if n.config.CFilterCDNAuto {
		baseURL, _, ok := defaultBlockDNProvider(n.config.Network)
		if ok {
			return baseURL
		}
	}
	return ""
}

// fetchBlockDNStatus fetches and decodes the /status endpoint of a block-dn
// CDN server. When useTor is true and a Tor proxy is configured, the request
// is routed through the SOCKS5 proxy.
func (n *Node) fetchBlockDNStatus(baseURL string, useTor bool) (*blockDNStatus, error) {
	httpClient := &http.Client{Timeout: defaultBlockDNStatusTimeout}

	if useTor && n.config.TorProxy != "" {
		torDialer, err := proxy.SOCKS5("tcp", n.config.TorProxy, nil, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("create Tor SOCKS5 dialer: %w", err)
		}
		httpClient = newTorHTTPClient(torDialer)
		httpClient.Timeout = defaultBlockDNStatusTimeout
	}

	statusURL := fmt.Sprintf("%s/status", strings.TrimRight(baseURL, "/"))
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, statusURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create status request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status endpoint returned HTTP %d", resp.StatusCode)
	}

	var status blockDNStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, fmt.Errorf("decode status response: %w", err)
	}

	return &status, nil
}

// defaultBlockDNProvider returns the block-dn base URL and expected chain name
// for the given network. Returns ok=false for unsupported networks.
func defaultBlockDNProvider(network string) (baseURL, chainName string, ok bool) {
	switch network {
	case "mainnet":
		return "https://block-dn.org", "mainnet", true
	case "signet":
		return "https://signet.block-dn.org", "signet", true
	case "testnet":
		return "https://testnet3.block-dn.org", "testnet3", true
	default:
		return "", "", false
	}
}

// parseCFilterChunk parses a block-dn binary cfilter chunk into individual raw
// filter byte slices. The format is a sequence of CompactSize-varint-prefixed
// raw GCS filter bytes concatenated together: [varint(len) | filter_bytes]...
//
// count is the expected number of filters. If fewer filters are found in the
// data, the function returns an error.
func parseCFilterChunk(data []byte, count int) ([][]byte, error) {
	filters := make([][]byte, 0, count)
	offset := 0

	for offset < len(data) {
		filterLen, bytesRead := readCompactSize(data[offset:])
		if bytesRead == 0 {
			return nil, fmt.Errorf("invalid CompactSize varint at offset %d", offset)
		}
		offset += bytesRead

		if offset+int(filterLen) > len(data) {
			return nil, fmt.Errorf(
				"filter at index %d: need %d bytes at offset %d, have %d",
				len(filters), filterLen, offset, len(data)-offset,
			)
		}

		filterData := make([]byte, filterLen)
		copy(filterData, data[offset:offset+int(filterLen)])
		filters = append(filters, filterData)
		offset += int(filterLen)
	}

	if len(filters) != count {
		return nil, fmt.Errorf("expected %d filters, got %d", count, len(filters))
	}

	return filters, nil
}

// readCompactSize reads a Bitcoin CompactSize unsigned integer from data.
// Returns the value and number of bytes consumed (0 if data is too short).
func readCompactSize(data []byte) (uint64, int) {
	if len(data) == 0 {
		return 0, 0
	}

	switch {
	case data[0] < 0xfd:
		return uint64(data[0]), 1
	case data[0] == 0xfd:
		if len(data) < 3 {
			return 0, 0
		}
		return uint64(data[1]) | uint64(data[2])<<8, 3
	case data[0] == 0xfe:
		if len(data) < 5 {
			return 0, 0
		}
		return uint64(data[1]) | uint64(data[2])<<8 |
			uint64(data[3])<<16 | uint64(data[4])<<24, 5
	default: // 0xff
		if len(data) < 9 {
			return 0, 0
		}
		return uint64(data[1]) | uint64(data[2])<<8 |
			uint64(data[3])<<16 | uint64(data[4])<<24 |
			uint64(data[5])<<32 | uint64(data[6])<<40 |
			uint64(data[7])<<48 | uint64(data[8])<<56, 9
	}
}

// downloadCFilterChunk downloads a single cfilter chunk from the CDN and
// imports the filters using ChainService.ImportCFilters.
func (n *Node) downloadCFilterChunk(httpClient *http.Client, baseURL string, startHeight int32, chunkSize int) (int, error) {
	url := fmt.Sprintf("%s/filters/%d", baseURL, startHeight)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("create request for height %d: %w", startHeight, err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("fetch filters at height %d: %w", startHeight, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("filters endpoint returned HTTP %d for height %d",
			resp.StatusCode, startHeight)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("read filter chunk at height %d: %w", startHeight, err)
	}

	rawFilters, err := parseCFilterChunk(body, chunkSize)
	if err != nil {
		return 0, fmt.Errorf("parse filter chunk at height %d: %w", startHeight, err)
	}

	imported, err := n.chainService.ImportCFilters(uint32(startHeight), rawFilters)
	if err != nil {
		return imported, fmt.Errorf("import filters at height %d: %w", startHeight, err)
	}

	return imported, nil
}

// cdnPrefetchFilters downloads and imports compact filters from the block-dn
// CDN. It determines the available range from the CDN status endpoint, then
// downloads chunks sequentially, verifying each against locally-stored filter
// headers. Returns the number of filters imported and the last height processed.
func (n *Node) cdnPrefetchFilters(startHeight int32, useTor bool) (imported int, lastHeight int32, err error) {
	baseURL := n.resolveCFilterCDNBaseURL()
	if baseURL == "" {
		return 0, startHeight - 1, nil
	}

	n.logger.Infof("CDN cfilter prefetch: resolving status from %s", baseURL)

	status, err := n.fetchBlockDNStatus(baseURL, useTor)
	if err != nil {
		return 0, startHeight - 1, fmt.Errorf("fetch CDN status: %w", err)
	}

	chunkSize := int(status.EntriesPerFilterFile)
	if chunkSize <= 0 {
		chunkSize = defaultBlockDNFilterChunkSize
	}

	// Determine CDN range: start must be aligned to chunk size.
	cdnStart := (startHeight / int32(chunkSize)) * int32(chunkSize)
	if cdnStart < startHeight {
		cdnStart += int32(chunkSize)
	}

	// CDN filter height is the last height available.
	cdnEnd := status.BestFilterHeight
	if cdnEnd <= 0 {
		cdnEnd = status.BestBlockHeight
	}
	// Align end to chunk boundary (last full chunk).
	cdnEnd = (cdnEnd / int32(chunkSize)) * int32(chunkSize)

	if cdnStart >= cdnEnd {
		n.logger.Infof("CDN cfilter prefetch: no chunks available (start=%d, cdn_end=%d)",
			startHeight, cdnEnd)
		return 0, startHeight - 1, nil
	}

	totalChunks := (cdnEnd - cdnStart) / int32(chunkSize)
	n.logger.Infof("CDN cfilter prefetch: downloading heights %d..%d (%d chunks of %d filters)",
		cdnStart, cdnEnd+int32(chunkSize)-1, totalChunks, chunkSize)

	// Create HTTP client (Tor-aware if needed).
	httpClient := &http.Client{Timeout: 2 * time.Minute}
	if useTor && n.config.TorProxy != "" {
		torDialer, torErr := proxy.SOCKS5("tcp", n.config.TorProxy, nil, proxy.Direct)
		if torErr != nil {
			return 0, startHeight - 1, fmt.Errorf(
				"create Tor dialer for CDN: %w", torErr,
			)
		}
		httpClient = newTorHTTPClient(torDialer)
		httpClient.Timeout = 5 * time.Minute // larger timeout for Tor
	}

	totalImported := 0
	lastProcessed := startHeight - 1
	started := time.Now()

	for height := cdnStart; height < cdnEnd; height += int32(chunkSize) {
		select {
		case <-n.prefetchQuit:
			n.logger.Info("CDN cfilter prefetch interrupted")
			return totalImported, lastProcessed, nil
		default:
		}

		chunkImported, chunkErr := n.downloadCFilterChunk(
			httpClient, baseURL, height, chunkSize,
		)
		totalImported += chunkImported

		if chunkErr != nil {
			n.logger.Warnf("CDN cfilter chunk at height %d failed: %v", height, chunkErr)
			// Stop CDN prefetch on error; P2P will fill remaining.
			return totalImported, lastProcessed, chunkErr
		}

		lastProcessed = height + int32(chunkSize) - 1
		elapsed := time.Since(started)
		chunksCompleted := (height-cdnStart)/int32(chunkSize) + 1
		n.logger.Infof("CDN cfilter prefetch: chunk %d/%d (height %d), %d filters imported, %s elapsed",
			chunksCompleted, totalChunks, height, totalImported, elapsed.Round(time.Second))
	}

	elapsed := time.Since(started)
	n.logger.Infof("CDN cfilter prefetch complete: %d filters imported in %s",
		totalImported, elapsed.Round(time.Second))

	return totalImported, lastProcessed, nil
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

// newTorHTTPClient creates an http.Client that routes all requests through the
// provided SOCKS5 dialer (typically a Tor proxy). This ensures HTTP header
// downloads do not leak the client's real IP address.
func newTorHTTPClient(torDialer proxy.Dialer) *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return torDialer.Dial(network, addr)
		},
	}
	return &http.Client{Transport: transport}
}
