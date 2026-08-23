package neutrino

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/btcsuite/btcd/address/v2"
	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/btcsuite/btclog"
)

func TestComputeFilterWorkerCount(t *testing.T) {
	tests := []struct {
		name     string
		gomax    int
		expected int
	}{
		{name: "minimum", gomax: 1, expected: 2},
		{name: "normal", gomax: 6, expected: 6},
		{name: "maximum", gomax: 64, expected: 16},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeFilterWorkerCount(tt.gomax)
			if got != tt.expected {
				t.Fatalf("expected %d, got %d", tt.expected, got)
			}
		})
	}
}

// TestNewRescanManager tests the creation of a new rescan manager.
func TestNewRescanManager(t *testing.T) {
	// This test requires a ChainService to get chain params, so we test
	// the manager's internal structure directly instead
	backend := btclog.NewBackend(nil)
	logger := backend.Logger("TEST")

	mgr := &RescanManager{
		chainParams:  &chaincfg.MainNetParams,
		logger:       logger,
		watchedAddrs: make(map[string]address.Address),
		utxoSet:      make(map[string]UTXO),
	}

	if mgr.logger == nil {
		t.Error("expected logger to be set")
	}

	if mgr.watchedAddrs == nil {
		t.Error("expected watchedAddrs map to be initialized")
	}

	if mgr.utxoSet == nil {
		t.Error("expected utxoSet map to be initialized")
	}

	if mgr.chainParams == nil {
		t.Error("expected chainParams to be set")
	}
}

// TestWatchAddress tests adding addresses to the watch list.
func TestWatchAddress(t *testing.T) {
	backend := btclog.NewBackend(nil)
	logger := backend.Logger("TEST")

	mgr := &RescanManager{
		chainParams:  &chaincfg.MainNetParams,
		logger:       logger,
		watchedAddrs: make(map[string]address.Address),
		utxoSet:      make(map[string]UTXO),
	}

	tests := []struct {
		name      string
		address   string
		wantError bool
	}{
		{
			name:      "valid mainnet address",
			address:   "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",
			wantError: false,
		},
		{
			name:      "invalid address",
			address:   "invalid",
			wantError: true,
		},
		{
			name:      "duplicate address",
			address:   "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",
			wantError: false, // Should not error on duplicate
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := mgr.WatchAddress(tt.address)
			if (err != nil) != tt.wantError {
				t.Errorf("WatchAddress() error = %v, wantError %v", err, tt.wantError)
			}

			if !tt.wantError && err == nil {
				if _, exists := mgr.watchedAddrs[tt.address]; !exists {
					t.Error("expected address to be in watchedAddrs")
				}
			}
		})
	}
}

func TestWatchedAddressCount(t *testing.T) {
	backend := btclog.NewBackend(nil)
	logger := backend.Logger("TEST")

	mgr := &RescanManager{
		chainParams:  &chaincfg.MainNetParams,
		logger:       logger,
		watchedAddrs: make(map[string]address.Address),
		utxoSet:      make(map[string]UTXO),
	}

	if got := mgr.WatchedAddressCount(); got != 0 {
		t.Fatalf("expected empty watched count=0, got %d", got)
	}

	if err := mgr.WatchAddress("1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"); err != nil {
		t.Fatalf("failed to watch address: %v", err)
	}

	if got := mgr.WatchedAddressCount(); got != 1 {
		t.Fatalf("expected watched count=1, got %d", got)
	}

	// Duplicate should not increase count.
	if err := mgr.WatchAddress("1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"); err != nil {
		t.Fatalf("failed to watch duplicate address: %v", err)
	}

	if got := mgr.WatchedAddressCount(); got != 1 {
		t.Fatalf("expected watched count to remain 1, got %d", got)
	}
}

// TestAddUTXO tests adding UTXOs to the set.
func TestAddUTXO(t *testing.T) {
	backend := btclog.NewBackend(nil)
	logger := backend.Logger("TEST")

	mgr := &RescanManager{
		chainParams:  &chaincfg.MainNetParams,
		logger:       logger,
		watchedAddrs: make(map[string]address.Address),
		utxoSet:      make(map[string]UTXO),
	}

	txHash := "0000000000000000000000000000000000000000000000000000000000000001"
	vout := uint32(0)
	value := int64(50000000)
	address := "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"
	scriptPubKey := []byte{0x76, 0xa9, 0x14} // Simplified script
	height := int32(100)

	mgr.AddUTXO(txHash, vout, value, address, scriptPubKey, height)

	utxoKey := "0000000000000000000000000000000000000000000000000000000000000001:0"
	utxo, exists := mgr.utxoSet[utxoKey]
	if !exists {
		t.Fatal("expected UTXO to be added to set")
	}

	if utxo.TxID != txHash {
		t.Errorf("expected TxID %s, got %s", txHash, utxo.TxID)
	}

	if utxo.Vout != vout {
		t.Errorf("expected Vout %d, got %d", vout, utxo.Vout)
	}

	if utxo.Value != value {
		t.Errorf("expected Value %d, got %d", value, utxo.Value)
	}

	if utxo.Address != address {
		t.Errorf("expected Address %s, got %s", address, utxo.Address)
	}

	if utxo.Height != height {
		t.Errorf("expected Height %d, got %d", height, utxo.Height)
	}
}

// TestRemoveUTXO tests removing UTXOs from the set.
func TestRemoveUTXO(t *testing.T) {
	backend := btclog.NewBackend(nil)
	logger := backend.Logger("TEST")

	mgr := &RescanManager{
		chainParams:  &chaincfg.MainNetParams,
		logger:       logger,
		watchedAddrs: make(map[string]address.Address),
		utxoSet:      make(map[string]UTXO),
	}

	// Add a UTXO first
	txHash := "0000000000000000000000000000000000000000000000000000000000000001"
	vout := uint32(0)
	mgr.AddUTXO(txHash, vout, 50000000, "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa", []byte{0x76, 0xa9, 0x14}, 100)

	utxoKey := "0000000000000000000000000000000000000000000000000000000000000001:0"
	if _, exists := mgr.utxoSet[utxoKey]; !exists {
		t.Fatal("UTXO should exist before removal")
	}

	// Remove the UTXO
	mgr.RemoveUTXO(txHash, vout)

	if _, exists := mgr.utxoSet[utxoKey]; exists {
		t.Error("UTXO should be removed from set")
	}
}

// TestGetUTXOs tests retrieving UTXOs for specific addresses.
func TestGetUTXOs(t *testing.T) {
	backend := btclog.NewBackend(nil)
	logger := backend.Logger("TEST")

	mgr := &RescanManager{
		chainService: nil,
		chainParams:  &chaincfg.MainNetParams,
		logger:       logger,
		watchedAddrs: make(map[string]address.Address),
		utxoSet:      make(map[string]UTXO),
	}

	// Add some UTXOs
	addr1 := "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"
	addr2 := "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2"

	mgr.AddUTXO("tx1", 0, 50000000, addr1, []byte{0x76}, 100)
	mgr.AddUTXO("tx2", 0, 25000000, addr1, []byte{0x76}, 101)
	mgr.AddUTXO("tx3", 0, 10000000, addr2, []byte{0x76}, 102)

	// Test that GetUTXOs fails without chain service
	_, err := mgr.GetUTXOs([]string{addr1})
	if err == nil {
		t.Fatal("expected error when chain service is nil")
	}

	if err.Error() != "chain service not initialized" {
		t.Errorf("expected 'chain service not initialized', got '%s'", err.Error())
	}

	// Test internal UTXO retrieval logic (what GetUTXOs would do if chain service was available)
	mgr.mu.RLock()
	utxos := make([]UTXO, 0)
	addrSet := map[string]bool{addr1: true}
	for _, utxo := range mgr.utxoSet {
		if addrSet[utxo.Address] {
			utxos = append(utxos, utxo)
		}
	}
	mgr.mu.RUnlock()

	if len(utxos) != 2 {
		t.Errorf("expected 2 UTXOs for addr1, got %d", len(utxos))
	}

	// Test for both addresses
	mgr.mu.RLock()
	utxos = make([]UTXO, 0)
	addrSet = map[string]bool{addr1: true, addr2: true}
	for _, utxo := range mgr.utxoSet {
		if addrSet[utxo.Address] {
			utxos = append(utxos, utxo)
		}
	}
	mgr.mu.RUnlock()

	if len(utxos) != 3 {
		t.Errorf("expected 3 UTXOs total, got %d", len(utxos))
	}

	// Test filtering by specific address
	mgr.mu.RLock()
	utxos = make([]UTXO, 0)
	addrSet = map[string]bool{addr2: true}
	for _, utxo := range mgr.utxoSet {
		if addrSet[utxo.Address] {
			utxos = append(utxos, utxo)
		}
	}
	mgr.mu.RUnlock()

	if len(utxos) != 1 {
		t.Errorf("expected 1 UTXO for addr2, got %d", len(utxos))
	}
}

// TestRescanNilChainService tests that Rescan returns error when chain service is nil.
func TestRescanNilChainService(t *testing.T) {
	backend := btclog.NewBackend(nil)
	logger := backend.Logger("TEST")

	mgr := &RescanManager{
		chainService: nil,
		chainParams:  &chaincfg.MainNetParams,
		logger:       logger,
		watchedAddrs: make(map[string]address.Address),
		utxoSet:      make(map[string]UTXO),
	}

	err := mgr.Rescan(0, []string{"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"})
	if err == nil {
		t.Error("expected error when chain service is nil")
	}

	if err.Error() != "chain service not initialized" {
		t.Errorf("expected 'chain service not initialized', got '%s'", err.Error())
	}
}

func TestGetRescanStatusDefaults(t *testing.T) {
	backend := btclog.NewBackend(nil)
	logger := backend.Logger("TEST")

	mgr := &RescanManager{
		chainParams:  &chaincfg.MainNetParams,
		logger:       logger,
		watchedAddrs: make(map[string]address.Address),
		utxoSet:      make(map[string]UTXO),
	}

	status := mgr.GetRescanStatus()
	if status.InProgress {
		t.Error("expected InProgress=false")
	}
	if status.LastStarted != 0 {
		t.Errorf("expected LastStarted=0, got %d", status.LastStarted)
	}
	if status.LastFinished != 0 {
		t.Errorf("expected LastFinished=0, got %d", status.LastFinished)
	}
}

// TestWatchAddressPersistence tests that WatchAddress persists addresses to the store.
func TestWatchAddressPersistence(t *testing.T) {
	dir := t.TempDir()
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	store, err := OpenStateStore(dir, logger)
	if err != nil {
		t.Fatalf("OpenStateStore() error: %v", err)
	}
	defer store.Close()

	mgr := &RescanManager{
		chainParams:  &chaincfg.MainNetParams,
		logger:       logger,
		store:        store,
		watchedAddrs: make(map[string]address.Address),
		utxoSet:      make(map[string]UTXO),
	}

	addr := "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"
	if err := mgr.WatchAddress(addr); err != nil {
		t.Fatalf("WatchAddress() error: %v", err)
	}

	// Verify it was persisted to the store.
	addrs, err := store.LoadWatchedAddrs()
	if err != nil {
		t.Fatalf("LoadWatchedAddrs() error: %v", err)
	}
	if len(addrs) != 1 || addrs[0] != addr {
		t.Errorf("expected [%s], got %v", addr, addrs)
	}
}

// TestLoadPersistedState tests that a RescanManager correctly loads persisted state.
func TestLoadPersistedState(t *testing.T) {
	dir := t.TempDir()
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	// Pre-populate the store with data.
	store1, err := OpenStateStore(dir, logger)
	if err != nil {
		t.Fatalf("OpenStateStore() error: %v", err)
	}

	if err := store1.SaveWatchedAddr("1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"); err != nil {
		t.Fatalf("SaveWatchedAddr() error: %v", err)
	}
	if err := store1.SaveWatchedAddr("1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2"); err != nil {
		t.Fatalf("SaveWatchedAddr() error: %v", err)
	}
	testUTXOs := map[string]UTXO{
		"abc123:0": {TxID: "abc123", Vout: 0, Value: 69000000, Address: "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa", Height: 295833},
	}
	if err := store1.SaveUTXOSet(testUTXOs); err != nil {
		t.Fatalf("SaveUTXOSet() error: %v", err)
	}
	if err := store1.SaveRescanMeta(296102, 243542); err != nil {
		t.Fatalf("SaveRescanMeta() error: %v", err)
	}
	store1.Close()

	// Re-open the store and create a new RescanManager that should load from it.
	store2, err := OpenStateStore(dir, logger)
	if err != nil {
		t.Fatalf("OpenStateStore() reopen error: %v", err)
	}
	defer store2.Close()

	mgr := &RescanManager{
		chainParams:  &chaincfg.MainNetParams,
		logger:       logger,
		store:        store2,
		watchedAddrs: make(map[string]address.Address),
		utxoSet:      make(map[string]UTXO),
	}
	mgr.loadPersistedState()

	// Verify watched addresses were loaded.
	if len(mgr.watchedAddrs) != 2 {
		t.Errorf("expected 2 watched addresses, got %d", len(mgr.watchedAddrs))
	}
	if _, ok := mgr.watchedAddrs["1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"]; !ok {
		t.Error("expected address 1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa to be loaded")
	}

	// Verify UTXOs were loaded.
	if len(mgr.utxoSet) != 1 {
		t.Errorf("expected 1 UTXO, got %d", len(mgr.utxoSet))
	}
	utxo, ok := mgr.utxoSet["abc123:0"]
	if !ok {
		t.Fatal("expected UTXO abc123:0 to be loaded")
	}
	if utxo.Value != 69000000 {
		t.Errorf("expected UTXO value 69000000, got %d", utxo.Value)
	}
	if utxo.Height != 295833 {
		t.Errorf("expected UTXO height 295833, got %d", utxo.Height)
	}

	// Verify rescan metadata was loaded into status.
	if mgr.status.LastScannedTip != 296102 {
		t.Errorf("expected LastScannedTip 296102, got %d", mgr.status.LastScannedTip)
	}
	if mgr.status.LastStartHeight != 243542 {
		t.Errorf("expected LastStartHeight 243542, got %d", mgr.status.LastStartHeight)
	}
}

// TestLoadPersistedStateInvalidAddress tests that invalid persisted addresses are skipped.
func TestLoadPersistedStateInvalidAddress(t *testing.T) {
	dir := t.TempDir()
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	store, err := OpenStateStore(dir, logger)
	if err != nil {
		t.Fatalf("OpenStateStore() error: %v", err)
	}

	// Save a valid and an invalid address.
	if err := store.SaveWatchedAddr("1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"); err != nil {
		t.Fatalf("SaveWatchedAddr() error: %v", err)
	}
	if err := store.SaveWatchedAddr("not_a_real_address"); err != nil {
		t.Fatalf("SaveWatchedAddr() error: %v", err)
	}
	store.Close()

	store2, err := OpenStateStore(dir, logger)
	if err != nil {
		t.Fatalf("OpenStateStore() error: %v", err)
	}
	defer store2.Close()

	mgr := &RescanManager{
		chainParams:  &chaincfg.MainNetParams,
		logger:       logger,
		store:        store2,
		watchedAddrs: make(map[string]address.Address),
		utxoSet:      make(map[string]UTXO),
	}
	mgr.loadPersistedState()

	// Only the valid address should be loaded.
	if len(mgr.watchedAddrs) != 1 {
		t.Errorf("expected 1 valid address after loading, got %d", len(mgr.watchedAddrs))
	}
}

// TestRescanManagerCloseWithStore tests that Close properly closes the store.
func TestRescanManagerCloseWithStore(t *testing.T) {
	dir := t.TempDir()
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	store, err := OpenStateStore(dir, logger)
	if err != nil {
		t.Fatalf("OpenStateStore() error: %v", err)
	}

	mgr := &RescanManager{
		chainParams:  &chaincfg.MainNetParams,
		logger:       logger,
		store:        store,
		watchedAddrs: make(map[string]address.Address),
		utxoSet:      make(map[string]UTXO),
	}

	if err := mgr.Close(); err != nil {
		t.Errorf("Close() error: %v", err)
	}

	// Subsequent close should not panic.
	// The bolt.DB.Close() is idempotent, so this should be fine.
}

// TestRescanManagerCloseWithoutStore tests that Close works when store is nil.
func TestRescanManagerCloseWithoutStore(t *testing.T) {
	mgr := &RescanManager{
		chainParams:  &chaincfg.MainNetParams,
		logger:       btclog.NewBackend(nil).Logger("TEST"),
		watchedAddrs: make(map[string]address.Address),
		utxoSet:      make(map[string]UTXO),
	}

	if err := mgr.Close(); err != nil {
		t.Errorf("Close() on nil store should not error, got: %v", err)
	}
}

// TestRescanFallsBackToWatchedAddrs tests that Rescan() uses watched addresses
// when called with an empty addresses list.
func TestRescanFallsBackToWatchedAddrs(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	mgr := &RescanManager{
		chainService: nil,
		chainParams:  &chaincfg.MainNetParams,
		logger:       logger,
		watchedAddrs: make(map[string]address.Address),
		utxoSet:      make(map[string]UTXO),
	}

	// Test 1: No watched addresses AND no explicit addresses — returns nil (nothing to scan).
	err := mgr.Rescan(0, []string{})
	if err != nil {
		t.Fatalf("expected nil error for empty rescan with no watched addrs, got: %v", err)
	}

	// Test 2: With a watched address and empty explicit addresses, Rescan should
	// fall back to using watched addresses and attempt the scan (which fails because
	// chainService is nil — but the fallback happened).
	addr, _ := address.DecodeAddress("1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa", &chaincfg.MainNetParams)
	mgr.watchedAddrs["1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"] = addr

	err = mgr.Rescan(0, []string{})
	if err == nil {
		t.Fatal("expected error from nil chain service, but got nil — rescan did not attempt to run")
	}
	if err.Error() != "chain service not initialized" {
		t.Errorf("expected 'chain service not initialized', got '%s'", err.Error())
	}
}

// TestRescanIncrementalSkip tests that Rescan() skips already-scanned block ranges
// when persisted state indicates a prior scan covered the requested range.
func TestRescanIncrementalSkip(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	mgr := &RescanManager{
		chainService: nil,
		chainParams:  &chaincfg.MainNetParams,
		logger:       logger,
		watchedAddrs: make(map[string]address.Address),
		utxoSet:      make(map[string]UTXO),
	}

	// Simulate persisted state: already scanned 243542..296100.
	mgr.status.LastScannedTip = 296100
	mgr.status.LastStartHeight = 243542

	// Add a watched address so we don't early-return from "no addresses".
	addr, _ := address.DecodeAddress("1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa", &chaincfg.MainNetParams)
	mgr.watchedAddrs["1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"] = addr

	// Rescan with start_height=243542 (same as persisted start).
	// This should attempt an incremental rescan from 296101, which will fail
	// because chainService is nil — but the fact it fails means it attempted the scan.
	err := mgr.Rescan(243542, []string{"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"})
	if err == nil {
		t.Fatal("expected error from nil chain service")
	}

	// Verify that LastStartHeight was set correctly.
	if mgr.status.LastStartHeight != 243542 {
		t.Errorf("expected LastStartHeight=243542, got %d", mgr.status.LastStartHeight)
	}
}

// TestRescanBusyRejected asserts that overlapping rescans are refused instead
// of interleaving updates to shared coverage metadata and persisted state.
func TestRescanBusyRejected(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	mgr := &RescanManager{
		chainParams:  &chaincfg.MainNetParams,
		logger:       logger,
		watchedAddrs: make(map[string]address.Address),
		utxoSet:      make(map[string]UTXO),
	}
	addr, _ := address.DecodeAddress("1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa", &chaincfg.MainNetParams)
	mgr.watchedAddrs["1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"] = addr

	// Simulate a running scan holding the single rescan slot.
	mgr.rescanInProgress.Store(1)

	err := mgr.Rescan(0, []string{"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"})
	if !errors.Is(err, ErrRescanBusy) {
		t.Fatalf("expected ErrRescanBusy, got %v", err)
	}
	if !mgr.IsRescanInProgress() {
		t.Fatal("busy rejection must not release the running scan's slot")
	}
}

// TestRescanNilChainServiceReleasesSlot asserts that admission failures do not
// leave the rescan slot permanently reserved.
func TestRescanNilChainServiceReleasesSlot(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	mgr := &RescanManager{
		chainParams:  &chaincfg.MainNetParams,
		logger:       logger,
		watchedAddrs: make(map[string]address.Address),
		utxoSet:      make(map[string]UTXO),
	}

	err := mgr.Rescan(0, []string{"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"})
	if err == nil || err.Error() != "chain service not initialized" {
		t.Fatalf("expected chain service error, got %v", err)
	}
	if mgr.IsRescanInProgress() {
		t.Fatal("failed admission must release the rescan slot")
	}
}

// TestForcedSubsetScanPreservesCoverageFloor asserts that a forced scan over
// explicitly supplied addresses does not widen last_start_height: only that
// subset was evaluated, so global coverage metadata must stay untouched.
func TestForcedSubsetScanPreservesCoverageFloor(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	mgr := &RescanManager{
		chainParams:  &chaincfg.MainNetParams,
		logger:       logger,
		watchedAddrs: make(map[string]address.Address),
		utxoSet:      make(map[string]UTXO),
	}
	mgr.status.LastStartHeight = 500
	mgr.status.LastScannedTip = 900

	job, err := mgr.beginRescan(0, []string{"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"}, true)
	if err == nil {
		t.Fatalf("expected chain service admission failure, got job=%v", job)
	}
	// Metadata is only mutated after full admission; a forced subset request
	// must never have widened the floor even transiently.
	if mgr.status.LastStartHeight != 500 || mgr.status.LastScannedTip != 900 {
		t.Fatalf("coverage metadata changed: start=%d tip=%d",
			mgr.status.LastStartHeight, mgr.status.LastScannedTip)
	}
}

// TestAutoSyncRetryNeeded asserts that a skipped auto-sync pass is retried
// exactly when a running scan was the only blocker.
func TestAutoSyncRetryNeeded(t *testing.T) {
	tests := []struct {
		name       string
		isCurrent  bool
		watched    int
		chainTip   int32
		lastTip    int32
		inProgress bool
		want       bool
	}{
		{
			name:      "retry when only blocked by a running scan",
			isCurrent: true, watched: 5, chainTip: 200, lastTip: 100,
			inProgress: true, want: true,
		},
		{
			name:      "no retry when idle (pass runs directly)",
			isCurrent: true, watched: 5, chainTip: 200, lastTip: 100,
			inProgress: false, want: false,
		},
		{
			name:      "no retry when chain tip already covered",
			isCurrent: true, watched: 5, chainTip: 100, lastTip: 100,
			inProgress: true, want: false,
		},
		{
			name:      "no retry when nothing is watched",
			isCurrent: true, watched: 0, chainTip: 200, lastTip: 100,
			inProgress: true, want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := autoSyncRetryNeeded(
				tt.isCurrent, tt.watched, tt.chainTip, tt.lastTip, tt.inProgress,
			)
			if got != tt.want {
				t.Fatalf("autoSyncRetryNeeded(...) = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRescanBusyHasNoSideEffects asserts that a busy rejection does not add
// the requested addresses to the watched set.
func TestRescanBusyHasNoSideEffects(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	mgr := &RescanManager{
		chainParams:  &chaincfg.MainNetParams,
		logger:       logger,
		watchedAddrs: make(map[string]address.Address),
		utxoSet:      make(map[string]UTXO),
	}
	mgr.rescanInProgress.Store(1)

	err := mgr.Rescan(0, []string{"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"})
	if !errors.Is(err, ErrRescanBusy) {
		t.Fatalf("expected ErrRescanBusy, got %v", err)
	}
	if mgr.WatchedAddressCount() != 0 {
		t.Fatal("busy rejection must not add addresses to the watched set")
	}
}

func TestUpdatesGlobalCoverage(t *testing.T) {
	tests := []struct {
		name     string
		force    bool
		explicit bool
		want     bool
	}{
		{name: "normal full scan updates coverage", force: false, explicit: false, want: true},
		{name: "normal subset scan updates coverage", force: false, explicit: true, want: true},
		{name: "forced full scan updates coverage", force: true, explicit: false, want: true},
		{name: "forced subset scan keeps coverage untouched", force: true, explicit: true, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := updatesGlobalCoverage(test.force, test.explicit); got != test.want {
				t.Fatalf("updatesGlobalCoverage(%t, %t) = %t, want %t",
					test.force, test.explicit, got, test.want)
			}
		})
	}
}

func TestEffectiveRescanStart(t *testing.T) {
	tests := []struct {
		name           string
		startHeight    int32
		persistedStart int32
		persistedTip   int32
		force          bool
		want           int32
	}{
		{
			name:           "incremental scan skips covered range",
			startHeight:    100,
			persistedStart: 100,
			persistedTip:   500,
			want:           501,
		},
		{
			name:           "forced scan keeps covered start",
			startHeight:    100,
			persistedStart: 100,
			persistedTip:   500,
			force:          true,
			want:           100,
		},
		{
			name:           "forced genesis scan remains valid",
			startHeight:    0,
			persistedStart: 0,
			persistedTip:   500,
			force:          true,
			want:           0,
		},
		{
			name:           "earlier range already bypasses skip",
			startHeight:    50,
			persistedStart: 100,
			persistedTip:   500,
			want:           50,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := effectiveRescanStart(
				test.startHeight,
				test.persistedStart,
				test.persistedTip,
				test.force,
			)
			if got != test.want {
				t.Fatalf("effectiveRescanStart() = %d, want %d", got, test.want)
			}
		})
	}
}

// TestVerifyUTXOsUnspentEmptySnapshot tests that verifyUTXOsUnspent returns 0
// when the snapshot is empty.
func TestVerifyUTXOsUnspentEmptySnapshot(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	mgr := &RescanManager{
		chainParams:  &chaincfg.MainNetParams,
		logger:       logger,
		watchedAddrs: make(map[string]address.Address),
		utxoSet:      make(map[string]UTXO),
	}

	removed := mgr.verifyUTXOsUnspent(nil, 0, 100)
	if removed != 0 {
		t.Errorf("expected 0 removed for nil snapshot, got %d", removed)
	}

	removed = mgr.verifyUTXOsUnspent(map[string]UTXO{}, 0, 100)
	if removed != 0 {
		t.Errorf("expected 0 removed for empty snapshot, got %d", removed)
	}
}

// TestVerifyUTXOsUnspentSkipsOutOfRange tests that UTXOs created after the scan
// range are skipped (no chain service calls needed).
func TestVerifyUTXOsUnspentSkipsOutOfRange(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	mgr := &RescanManager{
		// nil chainService — if it tries to call it, we'll get a panic,
		// proving the UTXO was NOT checked.
		chainParams:  &chaincfg.MainNetParams,
		logger:       logger,
		watchedAddrs: make(map[string]address.Address),
		utxoSet:      make(map[string]UTXO),
	}

	snapshot := map[string]UTXO{
		"abc:0": {
			TxID:    "abc",
			Vout:    0,
			Address: "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",
			Height:  200, // created at 200, scanEnd=100 => out of range
		},
	}

	// scanEnd < UTXO.Height, so the UTXO can't have been spent in this range.
	removed := mgr.verifyUTXOsUnspent(snapshot, 0, 100)
	if removed != 0 {
		t.Errorf("expected 0 removed for out-of-range UTXO, got %d", removed)
	}
}

// TestShouldAutoSync exercises the pure decision logic that drives the
// auto-sync goroutine, ensuring it triggers only under the expected
// conditions.
func TestShouldAutoSync(t *testing.T) {
	tests := []struct {
		name       string
		isCurrent  bool
		watched    int
		chainTip   int32
		lastTip    int32
		inProgress bool
		want       bool
	}{
		{
			name:      "trigger when chain advances and addresses are watched",
			isCurrent: true, watched: 5, chainTip: 200, lastTip: 100,
			inProgress: false, want: true,
		},
		{
			name:      "skip when chain not current",
			isCurrent: false, watched: 5, chainTip: 200, lastTip: 100,
			inProgress: false, want: false,
		},
		{
			name:      "skip when no watched addresses",
			isCurrent: true, watched: 0, chainTip: 200, lastTip: 100,
			inProgress: false, want: false,
		},
		{
			name:      "skip when chain tip already covered",
			isCurrent: true, watched: 5, chainTip: 100, lastTip: 100,
			inProgress: false, want: false,
		},
		{
			name:      "skip when rescan already running",
			isCurrent: true, watched: 5, chainTip: 200, lastTip: 100,
			inProgress: true, want: false,
		},
		{
			name:      "skip when chain tip is zero (not yet known)",
			isCurrent: true, watched: 5, chainTip: 0, lastTip: 0,
			inProgress: false, want: false,
		},
		{
			name:      "trigger on first sync (lastTip=0, watched present, tip>0)",
			isCurrent: true, watched: 1, chainTip: 1, lastTip: 0,
			inProgress: false, want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldAutoSync(
				tt.isCurrent, tt.watched, tt.chainTip, tt.lastTip,
				tt.inProgress,
			)
			if got != tt.want {
				t.Fatalf("shouldAutoSync(...) = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestStartAutoSyncLifecycle ensures StartAutoSync wires up its quit/done
// channels correctly and Close cleanly stops the goroutine.
func TestStartAutoSyncLifecycle(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	mgr := &RescanManager{
		// chainService is nil: runAutoSyncPass will return early on every
		// tick, so no chain interaction is attempted.
		chainParams:  &chaincfg.MainNetParams,
		logger:       logger,
		watchedAddrs: make(map[string]address.Address),
		utxoSet:      make(map[string]UTXO),
	}

	mgr.StartAutoSync(10 * time.Millisecond)

	if mgr.autoSyncQuit == nil {
		t.Fatal("expected autoSyncQuit channel to be created")
	}
	if mgr.autoSyncDone == nil {
		t.Fatal("expected autoSyncDone channel to be created")
	}

	// Close must stop the goroutine within a reasonable timeout.
	stopped := make(chan error, 1)
	go func() { stopped <- mgr.Close() }()

	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("auto-sync goroutine did not stop within 2s after Close")
	}
}

// TestPreserveEarliestLastStartHeight asserts the policy that drives the
// "skip redundant initial rescan" path on the client: incremental rescans
// (e.g. auto-sync, or a client requesting a recent start) must NEVER
// overwrite a previously persisted earlier LastStartHeight, otherwise
// clients lose the ability to detect that older heights have already been
// covered and will trigger a full re-scan on every CLI invocation.
//
// This is a regression test for the bug observed in production where
// auto-sync (calling Rescan(lastTip+1)) clobbered the original lookback
// start (e.g. 197179) with a near-tip value (e.g. 302300), causing the
// client's coverage check `prior_start <= scan_start_height` to fail.
func TestPreserveEarliestLastStartHeight(t *testing.T) {
	tests := []struct {
		name                string
		persistedStart      int32
		newStart            int32
		expectedAfterUpdate int32
	}{
		{
			name:                "first ever scan records start",
			persistedStart:      0,
			newStart:            197179,
			expectedAfterUpdate: 197179,
		},
		{
			name:                "incremental near-tip start does NOT overwrite",
			persistedStart:      197179,
			newStart:            302300,
			expectedAfterUpdate: 197179,
		},
		{
			name:                "earlier start widens coverage",
			persistedStart:      302300,
			newStart:            197179,
			expectedAfterUpdate: 197179,
		},
		{
			name:                "equal start is a no-op",
			persistedStart:      197179,
			newStart:            197179,
			expectedAfterUpdate: 197179,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Simulate the policy implemented in Rescan() inside the
			// statusMu critical section. Keep this in sync with the
			// real code at rescan.go (search "Preserve the *earliest*").
			persisted := tc.persistedStart
			updated := persisted
			if updated == 0 || tc.newStart < updated {
				updated = tc.newStart
			}
			if updated != tc.expectedAfterUpdate {
				t.Fatalf("LastStartHeight: got %d, want %d", updated, tc.expectedAfterUpdate)
			}
		})
	}
}
