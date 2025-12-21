/*
Package neutrino provides a wrapper around the lightninglabs/neutrino library.

This package initializes and manages a neutrino light client node that uses
BIP157/BIP158 compact block filters for privacy-preserving blockchain access.
*/
package neutrino

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/btcutil/gcs/builder"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btclog"
	"github.com/btcsuite/btcwallet/walletdb"
	_ "github.com/btcsuite/btcwallet/walletdb/bdb" // Import bbolt driver
	"github.com/lightninglabs/neutrino"
)

// Config holds configuration for the neutrino node.
type Config struct {
	Network         string
	DataDir         string
	TorProxy        string
	ConnectPeers    string
	MaxPeers        int
	BanDuration     time.Duration
	FilterCacheSize int
	Logger          *btclog.Backend
	LogLevel        string
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
	if config.ConnectPeers != "" {
		logger.Infof("Connect peers: %s", config.ConnectPeers)
	}

	node := &Node{
		config:      config,
		chainParams: chainParams,
		logger:      logger,
	}

	return node, nil
}

// Start initializes and starts the neutrino node.
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

	// Create neutrino config
	neutrinoConfig := neutrino.Config{
		DataDir:         n.config.DataDir,
		Database:        db,
		ChainParams:     *n.chainParams,
		FilterCacheSize: uint64(n.config.FilterCacheSize),
	}

	// Add peers if specified
	if n.config.ConnectPeers != "" {
		peers := strings.Split(n.config.ConnectPeers, ",")
		for _, peer := range peers {
			peer = strings.TrimSpace(peer)
			if peer != "" {
				n.logger.Infof("Adding connect peer: %s", peer)
				neutrinoConfig.ConnectPeers = append(neutrinoConfig.ConnectPeers, peer)
			}
		}
		n.logger.Infof("Total connect peers configured: %d", len(neutrinoConfig.ConnectPeers))
	}

	// Add DNS seeds if no connect peers specified
	if len(neutrinoConfig.ConnectPeers) == 0 {
		seeds := getDNSSeeds(n.config.Network)
		neutrinoConfig.AddPeers = seeds
		n.logger.Infof("No connect peers specified, using %d DNS seeds", len(seeds))
	}

	n.logger.Infof("Creating chain service for network: %s", n.chainParams.Name)

	// Create chain service
	chainService, err := neutrino.NewChainService(neutrinoConfig)
	if err != nil {
		n.db.Close()
		return fmt.Errorf("failed to create chain service: %w", err)
	}

	n.chainService = chainService
	n.logger.Info("Chain service created successfully")

	// Start the chain service
	n.logger.Info("Starting chain service...")
	if err := n.chainService.Start(); err != nil {
		n.db.Close()
		return fmt.Errorf("failed to start chain service: %w", err)
	}
	n.logger.Info("Chain service started successfully")

	// Create rescan manager
	n.rescanMgr = NewRescanManager(n.chainService, n.logger)

	// Start sync monitoring goroutine
	go n.monitorSync()

	n.logger.Info("Neutrino node started")
	return nil
}

// Stop gracefully stops the neutrino node.
func (n *Node) Stop() error {
	n.logger.Info("Stopping neutrino node...")

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

// UTXOSpendReport represents information about a UTXO.
type UTXOSpendReport struct {
	// If the output is unspent, these fields are populated
	Unspent      bool   `json:"unspent"`
	Value        int64  `json:"value,omitempty"`
	ScriptPubKey string `json:"scriptpubkey,omitempty"`

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
		return nil, errors.New("address is required: neutrino uses compact block filters which match on scripts, not outpoints")
	}

	// Parse the address to get the pkScript
	addr, err := btcutil.DecodeAddress(address, n.chainParams)
	if err != nil {
		return nil, fmt.Errorf("invalid address %s: %w", address, err)
	}

	pkScript, err := txscript.PayToAddrScript(addr)
	if err != nil {
		return nil, fmt.Errorf("failed to create script for address %s: %w", address, err)
	}

	// Parse txid
	targetHash, err := chainhash.NewHashFromStr(txid)
	if err != nil {
		return nil, fmt.Errorf("invalid txid: %w", err)
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
		return nil, fmt.Errorf("UTXO not found: ensure start_height is at or before the block containing the transaction")
	}

	report := &UTXOSpendReport{}

	if spendingTxHash == "" {
		// UTXO is unspent
		report.Unspent = true
		txOut := foundTx.TxOut[vout]
		report.Value = txOut.Value
		report.ScriptPubKey = fmt.Sprintf("%x", txOut.PkScript)
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
