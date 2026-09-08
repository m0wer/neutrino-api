/*
Package neutrino provides UTXO scanning using compact block filters.
*/
package neutrino

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/address/v2"
	"github.com/btcsuite/btcd/btcutil/v2"
	"github.com/btcsuite/btcd/btcutil/v2/gcs"
	"github.com/btcsuite/btcd/btcutil/v2/gcs/builder"
	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/txscript/v2"
	"github.com/btcsuite/btclog"
	"github.com/lightninglabs/neutrino"
	"github.com/lightninglabs/neutrino/blockntfns"
)

// RescanManager handles address watching and UTXO scanning.
type RescanManager struct {
	chainService *neutrino.ChainService
	chainParams  *chaincfg.Params
	logger       btclog.Logger
	store        *StateStore // nil means no persistence (e.g. in tests)

	mu           sync.RWMutex
	watchedAddrs map[string]address.Address
	utxoSet      map[string]UTXO // key: "txid:vout"

	// rescanInProgress tracks the number of active rescans (atomic).
	// Non-zero means a rescan goroutine is running.
	rescanInProgress atomic.Int32

	// autoSyncRetry records that an auto-sync pass was skipped because a
	// rescan owned the slot; the pass is re-run when the slot is released
	// so notification-driven catch-up work is never silently dropped.
	autoSyncRetry atomic.Bool

	statusMu sync.RWMutex
	status   RescanStatus

	// autoSyncQuit signals the auto-sync goroutine to stop. Created lazily
	// by StartAutoSync; nil when auto-sync is disabled.
	autoSyncQuit chan struct{}
	autoSyncDone chan struct{}

	// mempool is an optional reference to the watched-mempool tracker.
	// Set via SetMempoolTracker; nil when mempool tracking is disabled.
	// Guarded by mu (write on Set, read in notifyMempoolPostScan).
	mempool *MempoolTracker

	// txHistoryEnabled persists confirmed watched-tx records during scanning
	// (requires a non-nil store). Set via SetTxHistoryEnabled.
	txHistoryEnabled bool
}

// SetTxHistoryEnabled toggles persistence of confirmed watched-transaction
// history records during scanning.
func (r *RescanManager) SetTxHistoryEnabled(enabled bool) {
	r.mu.Lock()
	r.txHistoryEnabled = enabled
	r.mu.Unlock()
}

// TxHistorySince returns confirmed watched-transaction records with height
// strictly greater than sinceHeight. Returns nil when history is disabled or
// no store is configured.
func (r *RescanManager) TxHistorySince(sinceHeight int32) ([]TxHistoryRecord, error) {
	r.mu.RLock()
	store := r.store
	enabled := r.txHistoryEnabled
	r.mu.RUnlock()
	if store == nil || !enabled {
		return nil, nil
	}
	return store.LoadTxHistorySince(sinceHeight)
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
		watchedAddrs: make(map[string]address.Address),
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
			addr, err := address.DecodeAddress(addrStr, r.chainParams)
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
	// Stop the auto-sync goroutine first (if running) so it doesn't try to
	// touch the store after it is closed.
	if r.autoSyncQuit != nil {
		close(r.autoSyncQuit)
		<-r.autoSyncDone
		r.autoSyncQuit = nil
	}
	if r.store != nil {
		return r.store.Close()
	}
	return nil
}

// shouldAutoSync returns true when the auto-sync goroutine should trigger an
// incremental rescan. It is a pure function so it can be unit-tested without
// a live ChainService.
//
// Conditions for triggering:
//   - The chain service reports a sync state of "current" (isCurrent=true).
//   - At least one address is being watched.
//   - The chain tip is strictly ahead of the last scanned tip OR no scan has
//     ever run (lastScannedTip == 0) and watched addresses exist.
//   - No rescan is already in progress (avoids duplicate concurrent scans).
func shouldAutoSync(isCurrent bool, watchedCount int,
	chainTip, lastScannedTip int32, rescanInProgress bool,
) bool {
	if !isCurrent || watchedCount == 0 || rescanInProgress {
		return false
	}
	if chainTip <= 0 {
		return false
	}
	return chainTip > lastScannedTip
}

// StartAutoSync launches a background goroutine that subscribes to block
// notifications from the chain service and triggers an incremental rescan
// over all watched addresses whenever new blocks arrive. The goroutine exits
// when Close is called.
//
// The fallbackPollInterval is used while waiting for the chain service to
// finish initial sync (IsCurrent() == true) before subscribing, and as a
// safety-net poll if the subscription cannot be established.
//
// This method is safe to call only once per RescanManager instance.
func (r *RescanManager) StartAutoSync(fallbackPollInterval time.Duration) {
	if fallbackPollInterval <= 0 {
		fallbackPollInterval = 30 * time.Second
	}
	r.autoSyncQuit = make(chan struct{})
	r.autoSyncDone = make(chan struct{})

	go r.autoSyncLoop(fallbackPollInterval)
}

func (r *RescanManager) autoSyncLoop(fallbackPollInterval time.Duration) {
	defer close(r.autoSyncDone)

	r.logger.Info("Auto-sync goroutine started")

	// Phase 1: wait for the chain service to finish initial header/filter
	// sync before subscribing. Subscribing earlier could deliver a huge
	// backlog of historical blocks while the node is still catching up.
	if !r.waitForChainCurrent(fallbackPollInterval) {
		return
	}

	// Run an immediate first pass so the daemon catches up to the chain
	// tip on startup. This is critical: when the daemon restarts after
	// downtime, watched-address scanning happens here so the first
	// /v1/utxos query is instant.
	r.runAutoSyncPass()

	// Phase 2: subscribe to live block notifications. We pass 0 for
	// bestHeight so we only receive new blocks (no historical backlog --
	// the catch-up was already done above).
	src := &neutrino.RescanChainSource{ChainService: r.chainService}
	sub, err := src.Subscribe(0)
	if err != nil {
		r.logger.Warnf("Auto-sync: failed to subscribe to block notifications, falling back to polling every %s: %v",
			fallbackPollInterval, err)
		r.fallbackPollLoop(fallbackPollInterval)
		return
	}
	defer sub.Cancel()

	r.logger.Infof("Auto-sync: subscribed to live block notifications (initial scan complete)")

	for {
		select {
		case <-r.autoSyncQuit:
			r.logger.Info("Auto-sync goroutine stopping")
			return
		case ntfn, ok := <-sub.Notifications:
			if !ok {
				r.logger.Warnf("Auto-sync: block notification channel closed unexpectedly, falling back to polling")
				r.fallbackPollLoop(fallbackPollInterval)
				return
			}
			r.logger.Debugf("Auto-sync: received block notification at height %d",
				ntfn.Height())
			// Drain any additional notifications that arrived together
			// so we run a single incremental rescan over the whole
			// batch instead of one per block.
			r.drainPendingNotifications(sub.Notifications)
			r.runAutoSyncPass()
		}
	}
}

// waitForChainCurrent polls IsCurrent() until it returns true or the
// goroutine is asked to quit. Returns true if the chain became current,
// false if the goroutine should exit.
func (r *RescanManager) waitForChainCurrent(pollInterval time.Duration) bool {
	if r.chainService == nil {
		// No chain service (test harness): just block until quit.
		<-r.autoSyncQuit
		return false
	}

	if r.chainService.IsCurrent() {
		return true
	}

	r.logger.Info("Auto-sync: waiting for chain service to finish initial sync...")
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.autoSyncQuit:
			r.logger.Info("Auto-sync goroutine stopping (chain not yet current)")
			return false
		case <-ticker.C:
			if r.chainService.IsCurrent() {
				return true
			}
		}
	}
}

// drainPendingNotifications removes any notifications already queued on the
// channel without blocking. This batches bursts of block-connected events
// into a single rescan pass.
func (r *RescanManager) drainPendingNotifications(ch <-chan blockntfns.BlockNtfn) {
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		default:
			return
		}
	}
}

// fallbackPollLoop is used when block-notification subscription is
// unavailable. It performs a runAutoSyncPass on every tick.
func (r *RescanManager) fallbackPollLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.autoSyncQuit:
			r.logger.Info("Auto-sync goroutine stopping (fallback poll loop)")
			return
		case <-ticker.C:
			r.runAutoSyncPass()
		}
	}
}

// runAutoSyncPass performs a single auto-sync evaluation: if conditions are
// met, it triggers an incremental rescan over the current watched address set.
func (r *RescanManager) runAutoSyncPass() {
	if r.chainService == nil {
		return
	}

	isCurrent := r.chainService.IsCurrent()
	bestBlock, err := r.chainService.BestBlock()
	if err != nil {
		r.logger.Debugf("Auto-sync: failed to get best block: %v", err)
		return
	}

	r.statusMu.RLock()
	lastTip := r.status.LastScannedTip
	r.statusMu.RUnlock()

	watchedCount := r.WatchedAddressCount()

	inProgress := r.IsRescanInProgress()
	if !shouldAutoSync(isCurrent, watchedCount, bestBlock.Height, lastTip, inProgress) {
		if autoSyncRetryNeeded(isCurrent, watchedCount, bestBlock.Height, lastTip, inProgress) {
			// Another scan owns the slot right now. Remember this pass so
			// the catch-up runs when the slot is released; the triggering
			// block notification has already been consumed.
			r.autoSyncRetry.Store(true)
		}
		return
	}

	startHeight := lastTip + 1
	if startHeight <= 0 {
		// No prior scan: fall back to chain tip so we don't trigger a
		// massive sweep here. The client is expected to issue an
		// explicit /v1/rescan with the desired lookback when it first
		// registers a fresh wallet.
		startHeight = bestBlock.Height
	}

	r.logger.Infof("Auto-sync: chain advanced to height %d (last scanned %d), running incremental scan over %d watched addresses",
		bestBlock.Height, lastTip, watchedCount)

	if err := r.Rescan(startHeight, nil); err != nil {
		if errors.Is(err, ErrRescanBusy) {
			// Lost the admission race after the in-progress check; retry
			// once the winning scan releases the slot.
			r.autoSyncRetry.Store(true)
		}
		r.logger.Warnf("Auto-sync: incremental rescan failed: %v", err)
	}
}

// autoSyncRetryNeeded reports whether a skipped auto-sync pass must be
// retried after the currently running scan releases the slot: every trigger
// condition holds except that a scan is in progress.
func autoSyncRetryNeeded(isCurrent bool, watchedCount int,
	chainTip, lastScannedTip int32, rescanInProgress bool,
) bool {
	return rescanInProgress &&
		shouldAutoSync(isCurrent, watchedCount, chainTip, lastScannedTip, false)
}

// WatchAddress adds an address to the watch list.
func (r *RescanManager) WatchAddress(addrStr string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	addr, err := address.DecodeAddress(addrStr, r.chainParams)
	if err != nil {
		return fmt.Errorf("invalid address %s: %w", addrStr, err)
	}
	canonicalAddr := addr.String()
	if _, exists := r.watchedAddrs[canonicalAddr]; exists {
		return nil // Already watching
	}

	r.watchedAddrs[canonicalAddr] = addr
	r.logger.Debugf("Added watch address: %s", canonicalAddr)

	// Persist the new address.
	if r.store != nil {
		if err := r.store.SaveWatchedAddr(canonicalAddr); err != nil {
			r.logger.Warnf("Failed to persist watched address %s: %v", canonicalAddr, err)
			// Non-fatal: address is still in memory.
		}
	}

	return nil
}

const (
	// MaxWatchedAddressRemovalBatch bounds a single watched-address removal
	// request while allowing wallet registration batches well beyond normal
	// wallet gap limits.
	MaxWatchedAddressRemovalBatch = 1000
)

var (
	// ErrWatchedAddressRemovalEmpty is returned when no addresses were supplied
	// for removal.
	ErrWatchedAddressRemovalEmpty = errors.New("addresses must not be empty")
	// ErrWatchedAddressRemovalTooLarge is returned when a removal request exceeds
	// MaxWatchedAddressRemovalBatch input entries.
	ErrWatchedAddressRemovalTooLarge = fmt.Errorf(
		"addresses must contain at most %d entries", MaxWatchedAddressRemovalBatch,
	)
)

// WatchAddressRemoval reports the state removed by a batch watch deletion.
type WatchAddressRemoval struct {
	RemovedAddresses int `json:"removed_addresses"`
	RemovedUTXOs     int `json:"removed_utxos"`
}

// RemoveWatchedAddresses removes the supplied addresses and their confirmed
// UTXOs. Every input is validated before the rescan slot or any state is
// mutated, and the operation shares that slot with rescans to keep state
// transitions serialized.
func (r *RescanManager) RemoveWatchedAddresses(addresses []string) (WatchAddressRemoval, error) {
	addressSet, err := r.validateWatchedAddressRemoval(addresses)
	if err != nil {
		return WatchAddressRemoval{}, err
	}

	if !r.rescanInProgress.CompareAndSwap(0, 1) {
		return WatchAddressRemoval{}, ErrRescanBusy
	}
	defer r.releaseRescanSlot()

	r.mu.Lock()
	var persistedRemoval WatchAddressRemoval
	if r.store != nil {
		persistedRemoval, err = r.store.RemoveWatchedAddresses(addressSet)
		if err != nil {
			r.mu.Unlock()
			return WatchAddressRemoval{}, err
		}
	}

	memoryRemoval := WatchAddressRemoval{}
	for addr := range addressSet {
		if _, exists := r.watchedAddrs[addr]; exists {
			delete(r.watchedAddrs, addr)
			memoryRemoval.RemovedAddresses++
		}
	}

	removedOutpoints := make([]string, 0)
	for key, utxo := range r.utxoSet {
		if _, remove := addressSet[utxo.Address]; !remove {
			continue
		}
		delete(r.utxoSet, key)
		removedOutpoints = append(removedOutpoints, key)
		memoryRemoval.RemovedUTXOs++
	}
	mempool := r.mempool
	r.mu.Unlock()

	if mempool != nil {
		mempool.RemoveWatchedAddresses(addressSet, removedOutpoints)
	}

	removal := memoryRemoval
	if r.store != nil {
		// The API describes durable cleanup. Persisted state is canonical when
		// persistence is enabled, even if startup skipped a corrupt watch entry
		// or an older process left memory and disk out of sync.
		removal = persistedRemoval
	}
	r.logger.Infof("Removed %d watched addresses and %d UTXOs", removal.RemovedAddresses, removal.RemovedUTXOs)
	return removal, nil
}

func (r *RescanManager) validateWatchedAddressRemoval(addresses []string) (map[string]struct{}, error) {
	if len(addresses) == 0 {
		return nil, ErrWatchedAddressRemovalEmpty
	}
	if len(addresses) > MaxWatchedAddressRemovalBatch {
		return nil, ErrWatchedAddressRemovalTooLarge
	}

	addressSet := make(map[string]struct{}, len(addresses)*2)
	for _, addrStr := range addresses {
		decoded, err := address.DecodeAddress(addrStr, r.chainParams)
		if err != nil {
			return nil, fmt.Errorf("invalid address %s: %w", addrStr, err)
		}
		// Include both forms so deletion also cleans watches persisted by older
		// versions before WatchAddress canonicalized Bech32 strings.
		addressSet[addrStr] = struct{}{}
		addressSet[decoded.String()] = struct{}{}
	}
	return addressSet, nil
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
	for _, addrStr := range addresses {
		addrSet[addrStr] = true
		if addr, err := address.DecodeAddress(addrStr, r.chainParams); err == nil {
			addrSet[addr.String()] = true
		}
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

// WatchedAddressCount returns the number of currently watched addresses.
func (r *RescanManager) WatchedAddressCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.watchedAddrs)
}

// GetRescanStatus returns the current detailed rescan status.
func (r *RescanManager) GetRescanStatus() RescanStatus {
	r.statusMu.RLock()
	defer r.statusMu.RUnlock()

	status := r.status
	status.InProgress = r.IsRescanInProgress()
	return status
}

// ErrRescanBusy is returned when a rescan is requested while another rescan
// is still running. Rescans are serialized because they share the global
// coverage metadata and the persisted UTXO snapshot.
var ErrRescanBusy = errors.New("a rescan is already in progress")

// rescanJob describes a validated and admitted rescan operation.
type rescanJob struct {
	startHeight    int32
	addrs          []address.Address
	updateCoverage bool
	force          bool
}

// Rescan triggers a rescan from the given height for specified addresses.
// If addresses is empty, all watched addresses are used.
// This uses neutrino's block filter-based scanning.
func (r *RescanManager) Rescan(startHeight int32, addresses []string) error {
	job, err := r.beginRescan(startHeight, addresses, false)
	if err != nil || job == nil {
		return err
	}
	return r.runRescanJob(job)
}

// ForceRescan rescans the requested range even when global persisted coverage
// already includes it. This is required when callers add addresses after the
// original scan and need historical blocks evaluated against the new scripts.
func (r *RescanManager) ForceRescan(startHeight int32, addresses []string) error {
	job, err := r.beginRescan(startHeight, addresses, true)
	if err != nil || job == nil {
		return err
	}
	return r.runRescanJob(job)
}

// StartRescan validates and admits a rescan synchronously, then runs it in
// the background. When this returns nil the in-progress flag is already set,
// so callers can immediately poll GetRescanStatus for completion and
// last_error without racing the scan startup.
func (r *RescanManager) StartRescan(startHeight int32, addresses []string, force bool) error {
	job, err := r.beginRescan(startHeight, addresses, force)
	if err != nil || job == nil {
		return err
	}
	go func() {
		if err := r.runRescanJob(job); err != nil {
			r.logger.Errorf("Background rescan failed: %v", err)
		}
	}()
	return nil
}

// beginRescan validates the request, reserves the single rescan slot, and
// records the started lifecycle status. It returns (nil, nil) when there is
// nothing to scan.
func (r *RescanManager) beginRescan(startHeight int32, addresses []string, force bool) (*rescanJob, error) {
	// Serialize scans before any side effects: a busy rejection must not
	// leave requested addresses in the watched set without their
	// historical scan. Concurrent scans would also interleave updates to
	// the shared coverage metadata, and a slower scan could persist a
	// stale UTXO snapshot over a newer one.
	if !r.rescanInProgress.CompareAndSwap(0, 1) {
		return nil, ErrRescanBusy
	}
	releaseSlot := func() { r.releaseRescanSlot() }

	// Add addresses to watch list and collect address.Address objects.
	addrs := make([]address.Address, 0, len(addresses))
	for _, addrStr := range addresses {
		if err := r.WatchAddress(addrStr); err != nil {
			releaseSlot()
			return nil, err
		}
		decoded, err := address.DecodeAddress(addrStr, r.chainParams)
		if err != nil {
			releaseSlot()
			return nil, err
		}
		r.mu.RLock()
		addr := r.watchedAddrs[decoded.String()]
		r.mu.RUnlock()
		addrs = append(addrs, addr)
	}

	// Fall back to all watched addresses when none are explicitly provided.
	explicit := len(addrs) > 0
	if !explicit {
		r.mu.RLock()
		for _, addr := range r.watchedAddrs {
			addrs = append(addrs, addr)
		}
		r.mu.RUnlock()

		if len(addrs) == 0 {
			r.logger.Warn("Rescan called with no addresses and no watched addresses — nothing to scan")
			releaseSlot()
			return nil, nil
		}
		r.logger.Infof("Rescan called with no explicit addresses, using %d watched addresses", len(addrs))
	}

	if r.chainService == nil {
		releaseSlot()
		return nil, errors.New("chain service not initialized")
	}

	job := &rescanJob{
		startHeight: startHeight,
		addrs:       addrs,
		// A forced scan over explicitly supplied addresses backfills only
		// that subset, so it must not modify the global coverage metadata
		// that describes the whole watched set.
		updateCoverage: updatesGlobalCoverage(force, explicit),
		force:          force,
	}

	r.statusMu.Lock()
	r.status.LastStarted = time.Now().Unix()
	r.statusMu.Unlock()

	r.logger.Infof(
		"Starting rescan from height %d for %d addresses (force=%t)",
		startHeight, len(addrs), force,
	)
	return job, nil
}

// releaseRescanSlot frees the single rescan slot and replays a pending
// auto-sync pass that was skipped while the slot was held.
func (r *RescanManager) releaseRescanSlot() {
	r.rescanInProgress.Store(0)
	if r.autoSyncRetry.Swap(false) {
		go r.runAutoSyncPass()
	}
}

// runRescanJob executes an admitted rescan and releases the rescan slot.
func (r *RescanManager) runRescanJob(job *rescanJob) error {
	return r.runRescanJobWithSource(job, chainServiceUTXOScanSource{
		chainService: r.chainService,
	})
}

// runRescanJobWithSource executes an admitted rescan against a narrow chain
// data source. Keeping the source injectable makes scan-completeness failures
// testable without a live chain service.
func (r *RescanManager) runRescanJobWithSource(job *rescanJob, source utxoScanSource) error {
	defer r.releaseRescanSlot()

	// Determine effective start height: skip already-scanned ranges when possible.
	// If we have a persisted tip AND the requested start height falls within the
	// range we already scanned (startHeight >= LastStartHeight), we can resume
	// from LastScannedTip+1 instead of re-scanning the entire range.
	r.statusMu.RLock()
	persistedTip := r.status.LastScannedTip
	persistedStart := r.status.LastStartHeight
	r.statusMu.RUnlock()
	effectiveStart := effectiveRescanStart(job.startHeight, persistedStart, persistedTip, job.force)

	if effectiveStart != job.startHeight {
		r.logger.Infof("Incremental rescan: skipping already-scanned range %d..%d, resuming from %d",
			job.startHeight, persistedTip, effectiveStart)
	}

	// Get current best block.
	bestHeight, err := source.BestBlock()
	if err != nil {
		err = fmt.Errorf("failed to get best block: %w", err)
		r.recordRescanFailure(err)
		return err
	}
	if bestHeight < persistedTip {
		err = fmt.Errorf("chain tip changed during scan: current height %d is below prior coverage %d",
			bestHeight, persistedTip)
		r.recordRescanFailure(err)
		return err
	}

	// If effective start is already past the tip, nothing to scan.
	if effectiveStart > bestHeight {
		r.logger.Infof("Incremental rescan: already up-to-date (effective_start=%d, chain_tip=%d)",
			effectiveStart, bestHeight)
		r.statusMu.Lock()
		r.status.LastFinished = time.Now().Unix()
		if job.updateCoverage {
			r.status.LastScannedTip = bestHeight
		}
		r.status.LastError = ""
		r.statusMu.Unlock()
		r.persistState()
		// No blocks were scanned, so nothing in the watched mempool
		// view could have just confirmed: leave it intact.
		return nil
	}

	targetHash, err := source.GetBlockHash(int64(bestHeight))
	if err != nil {
		err = rescanDataUnavailable("target block hash", bestHeight, err)
		r.recordRescanFailure(err)
		return err
	}
	if targetHash == nil {
		err = rescanDataUnavailable("target block hash", bestHeight, nil)
		r.recordRescanFailure(err)
		return err
	}
	scanSource := newConsistentScanSource(source, bestHeight, *targetHash)

	// scanBlocks does not mutate state, so incomplete scans cannot
	// be committed as coverage.
	result, err := r.scanBlocks(effectiveStart, bestHeight, job.addrs, scanSource)
	if err == nil {
		err = scanSource.validate(context.Background())
	}
	if err == nil {
		result.verifiedTipHeight = bestHeight
		result.verifiedTipHash = targetHash.String()
		r.commitScanResult(result)
	}

	r.statusMu.Lock()
	r.status.LastFinished = time.Now().Unix()
	if err != nil {
		r.status.LastError = err.Error()
	} else {
		if job.updateCoverage {
			r.status.LastScannedTip = bestHeight
			if r.status.LastStartHeight == 0 || job.startHeight < r.status.LastStartHeight {
				r.status.LastStartHeight = job.startHeight
			}
		}
		r.status.LastError = ""
	}
	r.statusMu.Unlock()

	// Persist state after rescan completes (even on error, persist what we have).
	r.persistState()

	// After a successful pass, evict mempool entries that just confirmed.
	// Use the precise set surfaced by scanBlocks rather than EvictAll so
	// that still-unconfirmed tracked txs survive: mempool peers do not
	// reliably re-announce txs we have already acked, so a wholesale wipe
	// would silently drop tracked entries until they confirm.
	if err == nil && len(result.confirmedTxids) > 0 {
		r.notifyMempoolConfirmed(result.confirmedTxids)
	}

	return err
}

// recordRescanFailure surfaces an asynchronous scan failure to status pollers.
func (r *RescanManager) recordRescanFailure(err error) {
	r.statusMu.Lock()
	r.status.LastFinished = time.Now().Unix()
	r.status.LastError = err.Error()
	r.statusMu.Unlock()
}

func effectiveRescanStart(startHeight, persistedStart, persistedTip int32, force bool) int32 {
	if !force && persistedTip > 0 && startHeight >= persistedStart && startHeight <= persistedTip {
		return persistedTip + 1
	}
	return startHeight
}

// updatesGlobalCoverage reports whether a scan may update the persisted
// last_start_height/last_scanned_tip coverage metadata. Forced scans over an
// explicit address subset must not, because only those addresses were
// evaluated over the requested range.
func updatesGlobalCoverage(force bool, explicitAddresses bool) bool {
	return !force || !explicitAddresses
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

// scanResult contains all discoveries made during a completed scan. It is
// committed only after every required chain read, including spend verification,
// has succeeded.
type scanResult struct {
	confirmedTxids     []string
	foundUTXOs         map[string]UTXO
	spentOutputs       map[string]struct{}
	verifiedSpent      map[string]struct{}
	anchorAdvancements map[string]UTXO
	txHistory          map[string]*TxHistoryRecord
	verifiedTipHeight  int32
	verifiedTipHash    string
}

// consistentScanSource records every header used by a scan and verifies that
// each remains canonical before the scan result is committed.
type consistentScanSource struct {
	source       utxoScanSource
	targetHeight int32
	targetHash   chainhash.Hash

	mu       sync.Mutex
	observed map[int32]chainhash.Hash
}

func newConsistentScanSource(
	source utxoScanSource, targetHeight int32, targetHash chainhash.Hash,
) *consistentScanSource {
	return &consistentScanSource{
		source:       source,
		targetHeight: targetHeight,
		targetHash:   targetHash,
		observed:     map[int32]chainhash.Hash{targetHeight: targetHash},
	}
}

func (s *consistentScanSource) BestBlock() (int32, error) {
	return s.source.BestBlock()
}

func (s *consistentScanSource) GetBlockHash(height int64) (*chainhash.Hash, error) {
	blockHash, err := s.source.GetBlockHash(height)
	if err != nil || blockHash == nil {
		return blockHash, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	observedHash, ok := s.observed[int32(height)]
	if ok && !blockHash.IsEqual(&observedHash) {
		return nil, fmt.Errorf("chain changed during scan at height %d", height)
	}
	s.observed[int32(height)] = *blockHash
	return blockHash, nil
}

func (s *consistentScanSource) GetCFilter(blockHash chainhash.Hash) (*gcs.Filter, error) {
	return s.source.GetCFilter(blockHash)
}

func (s *consistentScanSource) GetBlock(blockHash chainhash.Hash) (*btcutil.Block, error) {
	return s.source.GetBlock(blockHash)
}

func (s *consistentScanSource) validate(ctx context.Context) error {
	s.mu.Lock()
	observed := make(map[int32]chainhash.Hash, len(s.observed))
	for height, blockHash := range s.observed {
		if height != s.targetHeight {
			observed[height] = blockHash
		}
	}
	s.mu.Unlock()

	for height, blockHash := range observed {
		if err := ctx.Err(); err != nil {
			return err
		}
		canonicalHash, err := s.source.GetBlockHash(int64(height))
		if err != nil {
			return rescanDataUnavailable("block hash validation", height, err)
		}
		if canonicalHash == nil {
			return rescanDataUnavailable("block hash validation", height, nil)
		}
		if !canonicalHash.IsEqual(&blockHash) {
			return fmt.Errorf("chain changed during scan at height %d", height)
		}
	}

	// Check the target last: a tip-only reorg during ancestor validation must
	// not leave the scan certified against a target checked before that reorg.
	if err := ctx.Err(); err != nil {
		return err
	}
	currentTip, err := s.source.BestBlock()
	if err != nil {
		return rescanDataUnavailable("chain tip validation", s.targetHeight, err)
	}
	if currentTip < s.targetHeight {
		return fmt.Errorf("chain tip changed during scan: current height %d is below target %d",
			currentTip, s.targetHeight)
	}
	canonicalTarget, err := s.source.GetBlockHash(int64(s.targetHeight))
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if err != nil || canonicalTarget == nil {
		return rescanDataUnavailable("target block hash validation", s.targetHeight, err)
	}
	if !canonicalTarget.IsEqual(&s.targetHash) {
		return fmt.Errorf("chain changed during scan at target height %d", s.targetHeight)
	}
	return nil
}

func rescanDataUnavailable(resource string, height int32, err error) error {
	if err != nil {
		return fmt.Errorf("%s unavailable at height %d: %w", resource, height, err)
	}
	return fmt.Errorf("%s unavailable at height %d", resource, height)
}

// scanBlocks scans the [startHeight, endHeight] range for transactions
// touching any of the supplied addresses. It returns staged discoveries that
// must be committed only after the complete scan succeeds.
func (r *RescanManager) scanBlocks(
	startHeight, endHeight int32, addrs []address.Address, source utxoScanSource,
) (*scanResult, error) {
	totalBlocks := endHeight - startHeight + 1
	r.logger.Infof("Scanning blocks %d to %d (%d blocks) for %d addresses",
		startHeight, endHeight, totalBlocks, len(addrs))

	// Build script filters for matching.
	scripts := make([][]byte, 0, len(addrs))
	addrToScript := make(map[string]string) // scriptHex -> address
	for _, addr := range addrs {
		script, err := txscript.PayToAddrScript(addr)
		if err != nil {
			return nil, fmt.Errorf("failed to create script for address %s: %w", addr.String(), err)
		}
		scripts = append(scripts, script)
		addrToScript[hex.EncodeToString(script)] = addr.String()
	}

	if len(scripts) == 0 {
		return nil, errors.New("no valid scripts to scan for")
	}

	// Progress tracking.
	scanStart := time.Now()
	lastProgressLog := scanStart
	blocksScanned := int32(0)
	filterMatches := 0
	const progressInterval = 10 * time.Second

	// Parallel phase 1: scan compact filters only. The collector drains every
	// worker result after an error, so the bounded worker pool always exits.
	workerCount := computeFilterWorkerCount(runtime.GOMAXPROCS(0))
	r.logger.Infof("Filter scan worker pool: %d workers", workerCount)

	type filterResult struct {
		height    int32
		matched   bool
		blockHash chainhash.Hash
		err       error
	}

	heightJobs := make(chan int32, workerCount*4)
	results := make(chan filterResult, workerCount*4)

	var wg sync.WaitGroup
	for range workerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for height := range heightJobs {
				blockHash, err := source.GetBlockHash(int64(height))
				if err != nil {
					results <- filterResult{height: height, err: rescanDataUnavailable("block hash", height, err)}
					continue
				}
				if blockHash == nil {
					results <- filterResult{height: height, err: rescanDataUnavailable("block hash", height, nil)}
					continue
				}

				filter, err := source.GetCFilter(*blockHash)
				if err != nil {
					results <- filterResult{height: height, err: rescanDataUnavailable("compact filter", height, err)}
					continue
				}
				if filter == nil {
					results <- filterResult{height: height, err: rescanDataUnavailable("compact filter", height, nil)}
					continue
				}

				key := builder.DeriveKey(blockHash)
				matched, err := filter.MatchAny(key, scripts)
				if err != nil {
					results <- filterResult{height: height, err: rescanDataUnavailable("compact filter match", height, err)}
					continue
				}

				results <- filterResult{height: height, matched: matched, blockHash: *blockHash}
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
	matchedBlockHashes := make(map[int32]chainhash.Hash)
	var scanErr error
	for res := range results {
		blocksScanned++
		if res.err != nil && scanErr == nil {
			scanErr = res.err
		}
		if res.matched {
			filterMatches++
			matchedHeights = append(matchedHeights, res.height)
			matchedBlockHashes[res.height] = res.blockHash
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
	if scanErr != nil {
		return nil, scanErr
	}

	// Snapshot watched outpoints and construct a local UTXO view. This view is
	// used for phase 3 and is not committed unless all subsequent reads succeed.
	r.mu.RLock()
	historyEnabled := r.txHistoryEnabled && r.store != nil
	utxoSnapshot := make(map[string]UTXO, len(r.utxoSet))
	priorUTXOs := make(map[string]UTXO, len(r.utxoSet))
	watchedOutpoints := make(map[string]struct{}, len(r.utxoSet))
	for key, utxo := range r.utxoSet {
		priorUTXOs[key] = utxo
		utxoSnapshot[key] = utxo
		if historyEnabled {
			watchedOutpoints[key] = struct{}{}
		}
	}
	r.mu.RUnlock()

	result := &scanResult{
		confirmedTxids:     make([]string, 0),
		foundUTXOs:         make(map[string]UTXO),
		spentOutputs:       make(map[string]struct{}),
		anchorAdvancements: make(map[string]UTXO),
		txHistory:          make(map[string]*TxHistoryRecord),
	}

	// Phase 2: fetch full blocks only for matched heights.
	slices.Sort(matchedHeights)
	for _, height := range matchedHeights {
		blockHash := matchedBlockHashes[height]

		r.logger.Debugf("Block %d filter matched, fetching full block", height)
		block, err := source.GetBlock(blockHash)
		if err != nil {
			return nil, rescanDataUnavailable("matched block", height, err)
		}
		if block == nil {
			return nil, rescanDataUnavailable("matched block", height, nil)
		}

		for _, tx := range block.Transactions() {
			txHash := tx.Hash().String()
			result.confirmedTxids = append(result.confirmedTxids, txHash)

			spendsWatched := false
			for _, txIn := range tx.MsgTx().TxIn {
				prevOut := txIn.PreviousOutPoint
				key := fmt.Sprintf("%s:%d", prevOut.Hash.String(), prevOut.Index)
				result.spentOutputs[key] = struct{}{}
				if historyEnabled {
					if _, ok := watchedOutpoints[key]; ok {
						spendsWatched = true
					}
				}
			}

			receiveAddrs := make([]string, 0)
			for vout, txOut := range tx.MsgTx().TxOut {
				scriptHex := hex.EncodeToString(txOut.PkScript)
				if addrStr, ok := addrToScript[scriptHex]; ok {
					utxoKey := fmt.Sprintf("%s:%d", txHash, vout)
					result.foundUTXOs[utxoKey] = UTXO{
						TxID:         txHash,
						Vout:         uint32(vout),
						Value:        txOut.Value,
						Address:      addrStr,
						ScriptPubKey: scriptHex,
						Height:       height,
					}
					receiveAddrs = append(receiveAddrs, addrStr)
					r.logger.Infof("Found UTXO: %s:%d value=%d address=%s height=%d",
						txHash, vout, txOut.Value, addrStr, height)
				}
			}

			if historyEnabled && (len(receiveAddrs) > 0 || spendsWatched) {
				r.recordTxHistory(result.txHistory, tx, txHash, height, receiveAddrs, spendsWatched)
			}
		}
	}

	// Apply the phase-2 discoveries to the local view before verifying spends.
	for utxoKey, utxo := range result.foundUTXOs {
		if _, spent := result.spentOutputs[utxoKey]; !spent {
			utxoSnapshot[utxoKey] = utxo
		}
	}
	for utxoKey := range result.spentOutputs {
		delete(utxoSnapshot, utxoKey)
	}

	// Phase 3: use a per-script scan to catch spends the batch MatchAny pass
	// can miss. Any unavailable data invalidates the whole scan result.
	verifiedSpent, err := r.verifyUTXOsUnspentWithSource(utxoSnapshot, startHeight, endHeight, source)
	if err != nil {
		return nil, err
	}
	result.verifiedSpent = verifiedSpent
	result.anchorAdvancements = r.anchorAdvancementCandidates(
		priorUTXOs, startHeight, endHeight, source,
	)

	elapsed := time.Since(scanStart)
	blocksPerSec := float64(0)
	if elapsed.Seconds() > 0 {
		blocksPerSec = float64(blocksScanned) / elapsed.Seconds()
	}
	r.logger.Infof("Scan complete: %d blocks in %s (%.1f blocks/sec) | %d filter matches | %d UTXOs found | %d verified spends",
		blocksScanned, elapsed.Round(time.Millisecond), blocksPerSec, filterMatches,
		len(result.foundUTXOs), len(result.verifiedSpent))

	return result, nil
}

// commitScanResult applies a complete scan result to the live and persisted
// views. Callers must not invoke it for a scan that returned an error.
func (r *RescanManager) commitScanResult(result *scanResult) {
	if len(result.txHistory) > 0 && r.store != nil {
		recs := make([]TxHistoryRecord, 0, len(result.txHistory))
		for _, rec := range result.txHistory {
			recs = append(recs, *rec)
		}
		if err := r.store.SaveTxHistoryBatch(recs); err != nil {
			r.logger.Warnf("Failed to persist %d tx history record(s): %v", len(recs), err)
		}
	}

	r.mu.Lock()
	added := 0
	for utxoKey, utxo := range result.foundUTXOs {
		if _, spent := result.spentOutputs[utxoKey]; !spent {
			utxo.VerifiedTipHeight = result.verifiedTipHeight
			utxo.VerifiedTipHash = result.verifiedTipHash
			r.utxoSet[utxoKey] = utxo
			added++
		}
	}

	removed := 0
	for utxoKey := range result.spentOutputs {
		if _, existed := r.utxoSet[utxoKey]; existed {
			delete(r.utxoSet, utxoKey)
			removed++
		}
	}
	for utxoKey := range result.verifiedSpent {
		if _, existed := r.utxoSet[utxoKey]; existed {
			delete(r.utxoSet, utxoKey)
			removed++
		}
	}
	for utxoKey, priorUTXO := range result.anchorAdvancements {
		currentUTXO, exists := r.utxoSet[utxoKey]
		if !exists || currentUTXO != priorUTXO {
			continue
		}
		currentUTXO.VerifiedTipHeight = result.verifiedTipHeight
		currentUTXO.VerifiedTipHash = result.verifiedTipHash
		r.utxoSet[utxoKey] = currentUTXO
	}
	total := len(r.utxoSet)
	r.mu.Unlock()

	r.logger.Infof("Committed scan result: %d UTXOs added, %d spent removed, %d total",
		added, removed, total)
}

// anchorAdvancementCandidates returns previously verified UTXOs whose proven
// range is contiguous with this scan. Missing or invalid old provenance is not
// upgraded by a suffix scan.
func (r *RescanManager) anchorAdvancementCandidates(
	utxos map[string]UTXO, scanStart, scanEnd int32, source utxoScanSource,
) map[string]UTXO {
	candidates := make(map[string]UTXO)
	for utxoKey, utxo := range utxos {
		if utxo.VerifiedTipHeight < utxo.Height || utxo.VerifiedTipHeight >= scanEnd ||
			scanStart > utxo.VerifiedTipHeight+1 || len(utxo.VerifiedTipHash) != chainhash.MaxHashStringSize {
			continue
		}
		verifiedTipHash, err := chainhash.NewHashFromStr(utxo.VerifiedTipHash)
		if err != nil {
			continue
		}
		canonicalHash, err := source.GetBlockHash(int64(utxo.VerifiedTipHeight))
		if err != nil || canonicalHash == nil || !canonicalHash.IsEqual(verifiedTipHash) {
			continue
		}
		candidates[utxoKey] = utxo
	}
	return candidates
}

// recordTxHistory merges a confirmed watched transaction into the per-scan
// history map, combining receive and spend involvement into one record.
func (r *RescanManager) recordTxHistory(
	txHistory map[string]*TxHistoryRecord,
	tx *btcutil.Tx,
	txHash string,
	height int32,
	receiveAddrs []string,
	spendsWatched bool,
) {
	rec, ok := txHistory[txHash]
	if !ok {
		var buf bytes.Buffer
		if err := tx.MsgTx().Serialize(&buf); err != nil {
			r.logger.Warnf("Failed to serialize tx %s for history: %v", txHash, err)
			return
		}
		rec = &TxHistoryRecord{
			TxID:      txHash,
			Hex:       hex.EncodeToString(buf.Bytes()),
			Height:    height,
			Confirmed: true,
		}
		txHistory[txHash] = rec
	}

	// Merge receive addresses (deduplicated) and the spend flag.
	seen := make(map[string]struct{}, len(rec.Addresses))
	for _, a := range rec.Addresses {
		seen[a] = struct{}{}
	}
	for _, a := range receiveAddrs {
		if _, dup := seen[a]; !dup {
			rec.Addresses = append(rec.Addresses, a)
			seen[a] = struct{}{}
		}
	}

	recv := len(rec.Addresses) > 0
	spend := spendsWatched || rec.Direction == "spend" || rec.Direction == "receive,spend"
	switch {
	case recv && spend:
		rec.Direction = "receive,spend"
	case spend:
		rec.Direction = "spend"
	default:
		rec.Direction = "receive"
	}
}

// verifyUTXOsUnspent performs a per-UTXO spend check and removes the proven
// spends. Scan jobs use verifyUTXOsUnspentWithSource so failures can abort
// before mutating the live UTXO set.
func (r *RescanManager) verifyUTXOsUnspent(
	snapshot map[string]UTXO, scanStart, scanEnd int32,
) int {
	if len(snapshot) == 0 {
		return 0
	}
	if r.chainService == nil {
		r.logger.Warn("Spend verification skipped: chain service is nil")
		return 0
	}

	spent, err := r.verifyUTXOsUnspentWithSource(snapshot, scanStart, scanEnd, chainServiceUTXOScanSource{
		chainService: r.chainService,
	})
	if err != nil {
		r.logger.Warnf("Spend verification failed: %v", err)
		return 0
	}

	removed := 0
	r.mu.Lock()
	for utxoKey := range spent {
		if _, exists := r.utxoSet[utxoKey]; exists {
			delete(r.utxoSet, utxoKey)
			removed++
		}
	}
	r.mu.Unlock()
	return removed
}

// verifyUTXOsUnspentWithSource performs the spend verification pass without
// mutating state. Every chain read is required to prove the resulting view is
// complete, so unavailable data returns an error instead of being skipped.
func (r *RescanManager) verifyUTXOsUnspentWithSource(
	snapshot map[string]UTXO, scanStart, scanEnd int32, source utxoScanSource,
) (map[string]struct{}, error) {
	spentUTXOs := make(map[string]struct{})
	if len(snapshot) == 0 {
		return spentUTXOs, nil
	}

	r.logger.Infof("Spend verification: checking %d UTXOs for spends in range %d..%d",
		len(snapshot), scanStart, scanEnd)
	verifyStart := time.Now()

	for utxoKey, utxo := range snapshot {
		// Only verify UTXOs that could have been spent in the scanned range.
		// A UTXO created after scanEnd cannot be spent in this range.
		// Start checking from the block AFTER the UTXO was created.
		checkFrom := utxo.Height + 1
		if checkFrom < scanStart {
			checkFrom = scanStart
		}
		if checkFrom > scanEnd {
			continue
		}

		addr, err := address.DecodeAddress(utxo.Address, r.chainParams)
		if err != nil {
			return nil, fmt.Errorf("spend verification address for UTXO %s: %w", utxoKey, err)
		}
		pkScript, err := txscript.PayToAddrScript(addr)
		if err != nil {
			return nil, fmt.Errorf("spend verification script for UTXO %s: %w", utxoKey, err)
		}

		targetHash, err := chainhash.NewHashFromStr(utxo.TxID)
		if err != nil {
			return nil, fmt.Errorf("spend verification txid for UTXO %s: %w", utxoKey, err)
		}

		for height := checkFrom; height <= scanEnd; height++ {
			blockHash, err := source.GetBlockHash(int64(height))
			if err != nil {
				return nil, rescanDataUnavailable("spend verification block hash", height, err)
			}
			if blockHash == nil {
				return nil, rescanDataUnavailable("spend verification block hash", height, nil)
			}

			filter, err := source.GetCFilter(*blockHash)
			if err != nil {
				return nil, rescanDataUnavailable("spend verification compact filter", height, err)
			}
			if filter == nil {
				return nil, rescanDataUnavailable("spend verification compact filter", height, nil)
			}

			key := builder.DeriveKey(blockHash)
			matched, err := filter.Match(key, pkScript)
			if err != nil {
				return nil, rescanDataUnavailable("spend verification compact filter match", height, err)
			}
			if !matched {
				continue
			}

			block, err := source.GetBlock(*blockHash)
			if err != nil {
				return nil, rescanDataUnavailable("spend verification block", height, err)
			}
			if block == nil {
				return nil, rescanDataUnavailable("spend verification block", height, nil)
			}

			for _, tx := range block.Transactions() {
				for _, txIn := range tx.MsgTx().TxIn {
					prevOut := txIn.PreviousOutPoint
					if prevOut.Hash.IsEqual(targetHash) && prevOut.Index == utxo.Vout {
						spentUTXOs[utxoKey] = struct{}{}
						r.logger.Infof("Spend verify: %s spent at height %d in tx %s",
							utxoKey, height, tx.Hash().String())
						break
					}
				}
				if _, spent := spentUTXOs[utxoKey]; spent {
					break
				}
			}
			if _, spent := spentUTXOs[utxoKey]; spent {
				break
			}
		}
	}

	r.logger.Infof("Spend verification complete: checked %d UTXOs in %s, found %d spent",
		len(snapshot), time.Since(verifyStart).Round(time.Millisecond), len(spentUTXOs))
	return spentUTXOs, nil
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
