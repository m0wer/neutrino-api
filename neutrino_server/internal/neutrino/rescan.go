/*
Package neutrino provides UTXO scanning using compact block filters.
*/
package neutrino

import (
	"bytes"
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
	"github.com/btcsuite/btcd/btcutil/v2/gcs/builder"
	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/txscript/v2"
	"github.com/btcsuite/btcd/wire/v2"
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
	// Preserve the *earliest* start height ever scanned so clients can
	// correctly determine coverage. Auto-sync passes always start at
	// (lastTip+1) which is far ahead of the original lookback start;
	// overwriting LastStartHeight with that value would erase the wallet's
	// historical coverage record and force a full re-scan on next CLI
	// invocation. Only widen coverage (lower the recorded start) -- never
	// narrow it.
	if job.updateCoverage &&
		(r.status.LastStartHeight == 0 || startHeight < r.status.LastStartHeight) {
		r.status.LastStartHeight = startHeight
	}
	r.status.LastError = ""
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
	bestBlock, err := r.chainService.BestBlock()
	if err != nil {
		err = fmt.Errorf("failed to get best block: %w", err)
		r.recordRescanFailure(err)
		return err
	}

	// If effective start is already past the tip, nothing to scan.
	if effectiveStart > bestBlock.Height {
		r.logger.Infof("Incremental rescan: already up-to-date (effective_start=%d, chain_tip=%d)",
			effectiveStart, bestBlock.Height)
		r.statusMu.Lock()
		r.status.LastFinished = time.Now().Unix()
		if job.updateCoverage {
			r.status.LastScannedTip = bestBlock.Height
		}
		r.statusMu.Unlock()
		r.persistState()
		// No blocks were scanned, so nothing in the watched mempool
		// view could have just confirmed: leave it intact.
		return nil
	}

	// Scan blocks from effectiveStart to bestBlock.Height
	confirmedTxids, err := r.scanBlocks(effectiveStart, bestBlock.Height, job.addrs)
	r.statusMu.Lock()
	r.status.LastFinished = time.Now().Unix()
	if job.updateCoverage {
		r.status.LastScannedTip = bestBlock.Height
	}
	if err != nil {
		r.status.LastError = err.Error()
	}
	r.statusMu.Unlock()

	// Persist state after rescan completes (even on error, persist what we have).
	r.persistState()

	// After a successful pass, evict mempool entries that just confirmed.
	// Use the precise set surfaced by scanBlocks rather than EvictAll so
	// that still-unconfirmed tracked txs survive: mempool peers do not
	// reliably re-announce txs we have already acked, so a wholesale wipe
	// would silently drop tracked entries until they confirm.
	if err == nil && len(confirmedTxids) > 0 {
		r.notifyMempoolConfirmed(confirmedTxids)
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

// scanBlocks scans blocks in the given range for transactions matching the addresses.
// scanBlocks scans the [startHeight, endHeight] range for transactions
// touching any of the supplied addresses. It returns the set of txids
// observed in matched blocks so callers can evict matching mempool entries.
// Note that the returned slice is over-eager: it includes every txid in any
// matched block, not only the ones that paid/spent a watched script. This
// is safe because consumers (MempoolTracker.EvictConfirmed) only act on
// txids they already track and silently ignore the rest.
func (r *RescanManager) scanBlocks(startHeight, endHeight int32, addrs []address.Address) ([]string, error) {
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
		return nil, errors.New("no valid scripts to scan for")
	}

	// Track spent outputs to remove from UTXO set
	spentOutputs := make(map[string]bool)
	// confirmedTxids collects every txid in any matched block, surfaced
	// to callers so a mempool tracker can evict precisely the entries
	// that just confirmed (instead of wiping its whole view).
	confirmedTxids := make([]string, 0)
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

	// Snapshot the pre-scan watched outpoints and the tx-history toggle so
	// the block loop can label a transaction as spending one of our coins
	// without holding the lock during network I/O.
	r.mu.RLock()
	historyEnabled := r.txHistoryEnabled && r.store != nil
	watchedOutpoints := make(map[string]struct{}, len(r.utxoSet))
	if historyEnabled {
		for key := range r.utxoSet {
			watchedOutpoints[key] = struct{}{}
		}
	}
	r.mu.RUnlock()

	// txHistory accumulates confirmed watched-tx records for this scan; keyed
	// by txid so receive+spend on the same tx merge into one record.
	txHistory := make(map[string]*TxHistoryRecord)

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
			confirmedTxids = append(confirmedTxids, txHash)

			spendsWatched := false
			for _, txIn := range tx.MsgTx().TxIn {
				prevOut := txIn.PreviousOutPoint
				key := fmt.Sprintf("%s:%d", prevOut.Hash.String(), prevOut.Index)
				spentOutputs[key] = true
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
					utxo := UTXO{
						TxID:         txHash,
						Vout:         uint32(vout),
						Value:        txOut.Value,
						Address:      addrStr,
						ScriptPubKey: scriptHex,
						Height:       height,
					}
					foundUTXOs[utxoKey] = utxo
					receiveAddrs = append(receiveAddrs, addrStr)
					r.logger.Infof("Found UTXO: %s:%d value=%d address=%s height=%d",
						txHash, vout, txOut.Value, addrStr, height)
				}
			}

			if historyEnabled && (len(receiveAddrs) > 0 || spendsWatched) {
				r.recordTxHistory(txHistory, tx, txHash, height, receiveAddrs, spendsWatched)
			}
		}
	}

	// Persist the confirmed watched-tx records for this scan.
	if historyEnabled && len(txHistory) > 0 {
		recs := make([]TxHistoryRecord, 0, len(txHistory))
		for _, rec := range txHistory {
			recs = append(recs, *rec)
		}
		if err := r.store.SaveTxHistoryBatch(recs); err != nil {
			r.logger.Warnf("Failed to persist %d tx history record(s): %v", len(recs), err)
		}
	}

	elapsed := time.Since(scanStart)

	// Update UTXO set (preliminary — before spend verification).
	r.mu.Lock()

	// Add new UTXOs (if not spent in this scan batch).
	added := 0
	for utxoKey, utxo := range foundUTXOs {
		if !spentOutputs[utxoKey] {
			r.utxoSet[utxoKey] = utxo
			added++
		}
	}

	// Remove spent UTXOs detected by the batch filter scan.
	removed := 0
	for utxoKey := range spentOutputs {
		if _, existed := r.utxoSet[utxoKey]; existed {
			delete(r.utxoSet, utxoKey)
			removed++
		}
	}

	// Snapshot the current UTXO set for the spend verification pass.
	// We need to release the lock before doing network I/O.
	utxoSnapshot := make(map[string]UTXO, len(r.utxoSet))
	for k, v := range r.utxoSet {
		utxoSnapshot[k] = v
	}
	r.mu.Unlock()

	blocksPerSec := float64(0)
	if elapsed.Seconds() > 0 {
		blocksPerSec = float64(blocksScanned) / elapsed.Seconds()
	}
	r.logger.Infof("Scan complete: %d blocks in %s (%.1f blocks/sec) | %d filter matches | %d UTXOs found, %d added, %d spent removed | total UTXO set: %d",
		blocksScanned, elapsed.Round(time.Millisecond), blocksPerSec,
		filterMatches, len(foundUTXOs), added, removed, len(r.utxoSet))

	// Phase 3: Spend verification pass.
	// The batch MatchAny filter scan can miss spends in certain cases (e.g.
	// subtle differences in multi-script vs single-script filter matching).
	// Verify each UTXO individually using per-script filter.Match, the same
	// approach used by GetUTXO which is known to be reliable.
	verifiedSpent := r.verifyUTXOsUnspent(utxoSnapshot, startHeight, endHeight)
	if verifiedSpent > 0 {
		r.logger.Infof("Spend verification removed %d additional spent UTXOs", verifiedSpent)
	}

	return confirmedTxids, nil
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

// verifyUTXOsUnspent performs a per-UTXO spend check for all UTXOs in the
// snapshot. For each UTXO, it scans block filters from the UTXO's creation
// height to endHeight using single-script filter.Match (not MatchAny). Any
// block that matches is downloaded and checked for transactions spending the
// UTXO. Confirmed spends are removed from r.utxoSet.
//
// This is a defense-in-depth measure: the main batch scan (MatchAny) should
// catch most spends, but this pass ensures none are missed.
func (r *RescanManager) verifyUTXOsUnspent(
	snapshot map[string]UTXO, scanStart, scanEnd int32,
) int {
	if len(snapshot) == 0 {
		return 0
	}

	r.logger.Infof("Spend verification: checking %d UTXOs for spends in range %d..%d",
		len(snapshot), scanStart, scanEnd)
	verifyStart := time.Now()
	removed := 0

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

		// Build the pkScript for this UTXO's address.
		addr, err := address.DecodeAddress(utxo.Address, r.chainParams)
		if err != nil {
			r.logger.Debugf("Spend verify: skip %s, bad address %s: %v",
				utxoKey, utxo.Address, err)
			continue
		}
		pkScript, err := txscript.PayToAddrScript(addr)
		if err != nil {
			r.logger.Debugf("Spend verify: skip %s, script error: %v",
				utxoKey, err)
			continue
		}

		// Parse the UTXO txid for outpoint comparison.
		targetHash, err := chainhash.NewHashFromStr(utxo.TxID)
		if err != nil {
			r.logger.Debugf("Spend verify: skip %s, bad txid: %v",
				utxoKey, err)
			continue
		}

		spent := false
		for height := checkFrom; height <= scanEnd && !spent; height++ {
			blockHash, err := r.chainService.GetBlockHash(int64(height))
			if err != nil {
				continue
			}

			filter, err := r.chainService.GetCFilter(*blockHash, wire.GCSFilterRegular)
			if err != nil || filter == nil {
				continue
			}

			key := builder.DeriveKey(blockHash)
			matched, err := filter.Match(key, pkScript)
			if err != nil || !matched {
				continue
			}

			// Filter matched for this single script — fetch full block.
			block, err := r.chainService.GetBlock(*blockHash)
			if err != nil {
				r.logger.Debugf("Spend verify: failed to get block %d: %v", height, err)
				continue
			}

			for _, tx := range block.Transactions() {
				for _, txIn := range tx.MsgTx().TxIn {
					prevOut := txIn.PreviousOutPoint
					if prevOut.Hash.IsEqual(targetHash) && prevOut.Index == utxo.Vout {
						spent = true
						r.logger.Infof("Spend verify: %s spent at height %d in tx %s",
							utxoKey, height, tx.Hash().String())
						break
					}
				}
				if spent {
					break
				}
			}
		}

		if spent {
			r.mu.Lock()
			if _, exists := r.utxoSet[utxoKey]; exists {
				delete(r.utxoSet, utxoKey)
				removed++
			}
			r.mu.Unlock()
		}
	}

	r.logger.Infof("Spend verification complete: checked %d UTXOs in %s, removed %d spent",
		len(snapshot), time.Since(verifyStart).Round(time.Millisecond), removed)
	return removed
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
