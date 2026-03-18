/*
Package neutrino provides UTXO scanning using compact block filters.
*/
package neutrino

import (
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/btcutil/gcs/builder"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btclog"
	"github.com/lightninglabs/neutrino"
)

// RescanManager handles address watching and UTXO scanning.
type RescanManager struct {
	chainService *neutrino.ChainService
	chainParams  *chaincfg.Params
	logger       btclog.Logger
	store        *StateStore // nil means no persistence (e.g. in tests)

	mu           sync.RWMutex
	watchedAddrs map[string]btcutil.Address
	utxoSet      map[string]UTXO // key: "txid:vout"

	// rescanInProgress tracks the number of active rescans (atomic).
	// Non-zero means a rescan goroutine is running.
	rescanInProgress atomic.Int32

	statusMu sync.RWMutex
	status   RescanStatus
}

// RescanStatus exposes rescan lifecycle details for API consumers.
type RescanStatus struct {
	InProgress      bool   `json:"in_progress"`
	LastStarted     int64  `json:"last_started"`
	LastFinished    int64  `json:"last_finished"`
	LastStartHeight int32  `json:"last_start_height"`
	LastScannedTip  int32  `json:"last_scanned_tip"`
	LastError       string `json:"last_error,omitempty"`
}

func computeFilterWorkerCount(gomax int) int {
	workerCount := gomax
	if workerCount < 2 {
		workerCount = 2
	}
	if workerCount > 16 {
		workerCount = 16
	}
	return workerCount
}

// NewRescanManager creates a new rescan manager.
// If store is non-nil, persisted state is loaded on creation.
func NewRescanManager(cs *neutrino.ChainService, logger btclog.Logger, store *StateStore) *RescanManager {
	chainParams := cs.ChainParams()
	mgr := &RescanManager{
		chainService: cs,
		chainParams:  &chainParams,
		logger:       logger,
		store:        store,
		watchedAddrs: make(map[string]btcutil.Address),
		utxoSet:      make(map[string]UTXO),
	}

	// Load persisted state if a store is available.
	if store != nil {
		mgr.loadPersistedState()
	}

	return mgr
}

// loadPersistedState restores watched addresses, UTXO set, and metadata from disk.
func (r *RescanManager) loadPersistedState() {
	// Load watched addresses.
	addrs, err := r.store.LoadWatchedAddrs()
	if err != nil {
		r.logger.Warnf("Failed to load watched addresses from store: %v", err)
	} else if len(addrs) > 0 {
		loaded := 0
		for _, addrStr := range addrs {
			addr, err := btcutil.DecodeAddress(addrStr, r.chainParams)
			if err != nil {
				r.logger.Warnf("Skipping invalid persisted address %s: %v", addrStr, err)
				continue
			}
			r.watchedAddrs[addrStr] = addr
			loaded++
		}
		r.logger.Infof("Loaded %d watched addresses from persistent store", loaded)
	}

	// Load UTXO set.
	utxos, err := r.store.LoadUTXOSet()
	if err != nil {
		r.logger.Warnf("Failed to load UTXO set from store: %v", err)
	} else if len(utxos) > 0 {
		r.utxoSet = utxos
		r.logger.Infof("Loaded %d UTXOs from persistent store", len(utxos))
	}

	// Load rescan metadata.
	tip, startHeight, err := r.store.LoadRescanMeta()
	if err != nil {
		r.logger.Warnf("Failed to load rescan metadata from store: %v", err)
	} else if tip > 0 {
		r.status.LastScannedTip = tip
		r.status.LastStartHeight = startHeight
		r.logger.Infof("Loaded rescan metadata: last_scanned_tip=%d, last_start_height=%d", tip, startHeight)
	}
}

// Close closes the underlying state store. Call this during shutdown.
func (r *RescanManager) Close() error {
	if r.store != nil {
		return r.store.Close()
	}
	return nil
}

// WatchAddress adds an address to the watch list.
func (r *RescanManager) WatchAddress(addrStr string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.watchedAddrs[addrStr]; exists {
		return nil // Already watching
	}

	addr, err := btcutil.DecodeAddress(addrStr, r.chainParams)
	if err != nil {
		return fmt.Errorf("invalid address %s: %w", addrStr, err)
	}

	r.watchedAddrs[addrStr] = addr
	r.logger.Debugf("Added watch address: %s", addrStr)

	// Persist the new address.
	if r.store != nil {
		if err := r.store.SaveWatchedAddr(addrStr); err != nil {
			r.logger.Warnf("Failed to persist watched address %s: %v", addrStr, err)
			// Non-fatal: address is still in memory.
		}
	}

	return nil
}

// GetUTXOs returns UTXOs for the given addresses.
// This performs a rescan using compact block filters if needed.
func (r *RescanManager) GetUTXOs(addresses []string) ([]UTXO, error) {
	if r.chainService == nil {
		return nil, errors.New("chain service not initialized")
	}

	// Add addresses to watch list
	for _, addr := range addresses {
		if err := r.WatchAddress(addr); err != nil {
			return nil, err
		}
	}

	// Collect UTXOs for the requested addresses
	r.mu.RLock()
	defer r.mu.RUnlock()

	utxos := make([]UTXO, 0)
	addrSet := make(map[string]bool)
	for _, addr := range addresses {
		addrSet[addr] = true
	}

	for _, utxo := range r.utxoSet {
		if addrSet[utxo.Address] {
			utxos = append(utxos, utxo)
		}
	}

	r.logger.Debugf("GetUTXOs returning %d UTXOs for %d addresses", len(utxos), len(addresses))
	return utxos, nil
}

// IsRescanInProgress returns true if a rescan goroutine is currently running.
func (r *RescanManager) IsRescanInProgress() bool {
	return r.rescanInProgress.Load() > 0
}

// GetRescanStatus returns the current detailed rescan status.
func (r *RescanManager) GetRescanStatus() RescanStatus {
	r.statusMu.RLock()
	defer r.statusMu.RUnlock()

	status := r.status
	status.InProgress = r.IsRescanInProgress()
	return status
}

// Rescan triggers a rescan from the given height for specified addresses.
// If addresses is empty, all watched addresses are used.
// This uses neutrino's block filter-based scanning.
func (r *RescanManager) Rescan(startHeight int32, addresses []string) error {
	// Add addresses to watch list and collect btcutil.Address objects
	addrs := make([]btcutil.Address, 0, len(addresses))
	for _, addrStr := range addresses {
		if err := r.WatchAddress(addrStr); err != nil {
			return err
		}
		r.mu.RLock()
		addr := r.watchedAddrs[addrStr]
		r.mu.RUnlock()
		addrs = append(addrs, addr)
	}

	// Fall back to all watched addresses when none are explicitly provided.
	if len(addrs) == 0 {
		r.mu.RLock()
		for _, addr := range r.watchedAddrs {
			addrs = append(addrs, addr)
		}
		r.mu.RUnlock()

		if len(addrs) == 0 {
			r.logger.Warn("Rescan called with no addresses and no watched addresses — nothing to scan")
			return nil
		}
		r.logger.Infof("Rescan called with no explicit addresses, using %d watched addresses", len(addrs))
	}

	// Now that we know we have addresses to scan, verify chain service is available.
	if r.chainService == nil {
		return errors.New("chain service not initialized")
	}

	r.logger.Infof("Starting rescan from height %d for %d addresses", startHeight, len(addrs))

	// Determine effective start height: skip already-scanned ranges when possible.
	// If we have a persisted tip AND the requested start height falls within the
	// range we already scanned (startHeight >= LastStartHeight), we can resume
	// from LastScannedTip+1 instead of re-scanning the entire range.
	effectiveStart := startHeight
	r.statusMu.RLock()
	persistedTip := r.status.LastScannedTip
	persistedStart := r.status.LastStartHeight
	r.statusMu.RUnlock()

	if persistedTip > 0 && startHeight >= persistedStart && effectiveStart <= persistedTip {
		effectiveStart = persistedTip + 1
		r.logger.Infof("Incremental rescan: skipping already-scanned range %d..%d, resuming from %d",
			startHeight, persistedTip, effectiveStart)
	}

	r.statusMu.Lock()
	r.status.LastStarted = time.Now().Unix()
	r.status.LastStartHeight = startHeight
	r.status.LastError = ""
	r.statusMu.Unlock()

	// Mark rescan as in-progress so callers can poll /v1/rescan/status.
	r.rescanInProgress.Add(1)
	defer r.rescanInProgress.Add(-1)

	// Get current best block
	bestBlock, err := r.chainService.BestBlock()
	if err != nil {
		return fmt.Errorf("failed to get best block: %w", err)
	}

	// If effective start is already past the tip, nothing to scan.
	if effectiveStart > bestBlock.Height {
		r.logger.Infof("Incremental rescan: already up-to-date (effective_start=%d, chain_tip=%d)",
			effectiveStart, bestBlock.Height)
		r.statusMu.Lock()
		r.status.LastFinished = time.Now().Unix()
		r.status.LastScannedTip = bestBlock.Height
		r.statusMu.Unlock()
		r.persistState()
		return nil
	}

	// Scan blocks from effectiveStart to bestBlock.Height
	err = r.scanBlocks(effectiveStart, bestBlock.Height, addrs)
	r.statusMu.Lock()
	r.status.LastFinished = time.Now().Unix()
	r.status.LastScannedTip = bestBlock.Height
	if err != nil {
		r.status.LastError = err.Error()
	}
	r.statusMu.Unlock()

	// Persist state after rescan completes (even on error, persist what we have).
	r.persistState()

	return err
}

// persistState saves the current UTXO set and rescan metadata to the store.
func (r *RescanManager) persistState() {
	if r.store == nil {
		return
	}

	r.mu.RLock()
	utxosCopy := make(map[string]UTXO, len(r.utxoSet))
	for k, v := range r.utxoSet {
		utxosCopy[k] = v
	}
	r.mu.RUnlock()

	r.statusMu.RLock()
	tip := r.status.LastScannedTip
	startHeight := r.status.LastStartHeight
	r.statusMu.RUnlock()

	if err := r.store.SaveUTXOSet(utxosCopy); err != nil {
		r.logger.Warnf("Failed to persist UTXO set: %v", err)
	} else {
		r.logger.Infof("Persisted %d UTXOs to state store", len(utxosCopy))
	}

	if err := r.store.SaveRescanMeta(tip, startHeight); err != nil {
		r.logger.Warnf("Failed to persist rescan metadata: %v", err)
	} else {
		r.logger.Infof("Persisted rescan metadata: last_scanned_tip=%d, last_start_height=%d", tip, startHeight)
	}
}

// scanBlocks scans blocks in the given range for transactions matching the addresses.
func (r *RescanManager) scanBlocks(startHeight, endHeight int32, addrs []btcutil.Address) error {
	totalBlocks := endHeight - startHeight + 1
	r.logger.Infof("Scanning blocks %d to %d (%d blocks) for %d addresses",
		startHeight, endHeight, totalBlocks, len(addrs))

	// Build script filters for matching
	scripts := make([][]byte, 0, len(addrs))
	addrToScript := make(map[string]string) // scriptHex -> address
	for _, addr := range addrs {
		script, err := txscript.PayToAddrScript(addr)
		if err != nil {
			r.logger.Warnf("Failed to create script for address %s: %v", addr.String(), err)
			continue
		}
		scripts = append(scripts, script)
		addrToScript[hex.EncodeToString(script)] = addr.String()
	}

	if len(scripts) == 0 {
		return errors.New("no valid scripts to scan for")
	}

	// Track spent outputs to remove from UTXO set
	spentOutputs := make(map[string]bool)
	foundUTXOs := make(map[string]UTXO)

	// Progress tracking
	scanStart := time.Now()
	lastProgressLog := scanStart
	blocksScanned := int32(0)
	filterMatches := 0
	const progressInterval = 10 * time.Second

	// Parallel phase 1: scan compact filters only.
	// This is the bottleneck and can safely run concurrently.
	workerCount := computeFilterWorkerCount(runtime.GOMAXPROCS(0))
	r.logger.Infof("Filter scan worker pool: %d workers", workerCount)

	type filterResult struct {
		height  int32
		matched bool
	}

	heightJobs := make(chan int32, workerCount*4)
	results := make(chan filterResult, workerCount*4)

	var wg sync.WaitGroup
	for range workerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for height := range heightJobs {
				blockHash, err := r.chainService.GetBlockHash(int64(height))
				if err != nil {
					r.logger.Debugf("Failed to get block hash for height %d: %v", height, err)
					results <- filterResult{height: height, matched: false}
					continue
				}

				filter, err := r.chainService.GetCFilter(*blockHash, wire.GCSFilterRegular)
				if err != nil {
					r.logger.Debugf("Failed to get filter for block %d: %v", height, err)
					results <- filterResult{height: height, matched: false}
					continue
				}

				if filter == nil {
					results <- filterResult{height: height, matched: false}
					continue
				}

				key := builder.DeriveKey(blockHash)
				matched, err := filter.MatchAny(key, scripts)
				if err != nil {
					r.logger.Debugf("Filter match error for block %d: %v", height, err)
					results <- filterResult{height: height, matched: false}
					continue
				}

				results <- filterResult{height: height, matched: matched}
			}
		}()
	}

	go func() {
		for height := startHeight; height <= endHeight; height++ {
			heightJobs <- height
		}
		close(heightJobs)
		wg.Wait()
		close(results)
	}()

	matchedHeights := make([]int32, 0, 16)
	for res := range results {
		blocksScanned++
		if res.matched {
			filterMatches++
			matchedHeights = append(matchedHeights, res.height)
		}

		now := time.Now()
		if now.Sub(lastProgressLog) >= progressInterval {
			elapsed := now.Sub(scanStart)
			pct := float64(blocksScanned) / float64(totalBlocks) * 100
			blocksPerSec := float64(blocksScanned) / elapsed.Seconds()
			remaining := int32(0)
			if blocksPerSec > 0 {
				remaining = int32(float64(totalBlocks-blocksScanned) / blocksPerSec)
			}
			r.logger.Infof("Scan progress: %d/%d blocks (%.1f%%) | height %d | %.1f blocks/sec | ~%ds remaining | %d filter matches",
				blocksScanned, totalBlocks, pct, res.height, blocksPerSec, remaining, filterMatches)
			lastProgressLog = now
		}
	}

	// Phase 2: fetch full blocks only for matched heights.
	slices.Sort(matchedHeights)
	for _, height := range matchedHeights {
		blockHash, err := r.chainService.GetBlockHash(int64(height))
		if err != nil {
			r.logger.Debugf("Failed to get block hash for matched block %d: %v", height, err)
			continue
		}

		r.logger.Debugf("Block %d filter matched, fetching full block", height)
		block, err := r.chainService.GetBlock(*blockHash)
		if err != nil {
			r.logger.Warnf("Failed to get block %d: %v", height, err)
			continue
		}

		for _, tx := range block.Transactions() {
			txHash := tx.Hash().String()

			for _, txIn := range tx.MsgTx().TxIn {
				prevOut := txIn.PreviousOutPoint
				key := fmt.Sprintf("%s:%d", prevOut.Hash.String(), prevOut.Index)
				spentOutputs[key] = true
			}

			for vout, txOut := range tx.MsgTx().TxOut {
				scriptHex := hex.EncodeToString(txOut.PkScript)
				if addrStr, ok := addrToScript[scriptHex]; ok {
					utxoKey := fmt.Sprintf("%s:%d", txHash, vout)
					utxo := UTXO{
						TxID:         txHash,
						Vout:         uint32(vout),
						Value:        txOut.Value,
						Address:      addrStr,
						ScriptPubKey: scriptHex,
						Height:       height,
					}
					foundUTXOs[utxoKey] = utxo
					r.logger.Infof("Found UTXO: %s:%d value=%d address=%s height=%d",
						txHash, vout, txOut.Value, addrStr, height)
				}
			}
		}
	}

	elapsed := time.Since(scanStart)

	// Update UTXO set
	r.mu.Lock()
	defer r.mu.Unlock()

	// Add new UTXOs (if not spent)
	added := 0
	for utxoKey, utxo := range foundUTXOs {
		if !spentOutputs[utxoKey] {
			r.utxoSet[utxoKey] = utxo
			added++
		}
	}

	// Remove spent UTXOs
	removed := 0
	for utxoKey := range spentOutputs {
		if _, existed := r.utxoSet[utxoKey]; existed {
			delete(r.utxoSet, utxoKey)
			removed++
		}
	}

	blocksPerSec := float64(0)
	if elapsed.Seconds() > 0 {
		blocksPerSec = float64(blocksScanned) / elapsed.Seconds()
	}
	r.logger.Infof("Scan complete: %d blocks in %s (%.1f blocks/sec) | %d filter matches | %d UTXOs found, %d added, %d spent removed | total UTXO set: %d",
		blocksScanned, elapsed.Round(time.Millisecond), blocksPerSec,
		filterMatches, len(foundUTXOs), added, removed, len(r.utxoSet))
	return nil
}

// AddUTXO adds a UTXO to the set (for use by notification handlers).
func (r *RescanManager) AddUTXO(txHash string, vout uint32, value int64, addrStr string, scriptPubKey []byte, height int32) {
	r.mu.Lock()
	defer r.mu.Unlock()

	utxoKey := fmt.Sprintf("%s:%d", txHash, vout)
	utxo := UTXO{
		TxID:         txHash,
		Vout:         vout,
		Value:        value,
		Address:      addrStr,
		ScriptPubKey: hex.EncodeToString(scriptPubKey),
		Height:       height,
	}

	r.utxoSet[utxoKey] = utxo
	r.logger.Debugf("Added UTXO: %s", utxoKey)
}

// RemoveUTXO removes a spent UTXO from the set.
func (r *RescanManager) RemoveUTXO(txid string, vout uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()

	utxoKey := fmt.Sprintf("%s:%d", txid, vout)
	delete(r.utxoSet, utxoKey)
	r.logger.Debugf("Removed UTXO: %s", utxoKey)
}
