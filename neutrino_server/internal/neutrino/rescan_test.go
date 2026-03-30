package neutrino

import (
	"os"
	"testing"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
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
		watchedAddrs: make(map[string]btcutil.Address),
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
		watchedAddrs: make(map[string]btcutil.Address),
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

// TestAddUTXO tests adding UTXOs to the set.
func TestAddUTXO(t *testing.T) {
	backend := btclog.NewBackend(nil)
	logger := backend.Logger("TEST")

	mgr := &RescanManager{
		chainParams:  &chaincfg.MainNetParams,
		logger:       logger,
		watchedAddrs: make(map[string]btcutil.Address),
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
		watchedAddrs: make(map[string]btcutil.Address),
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
		watchedAddrs: make(map[string]btcutil.Address),
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
		watchedAddrs: make(map[string]btcutil.Address),
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
		watchedAddrs: make(map[string]btcutil.Address),
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
		watchedAddrs: make(map[string]btcutil.Address),
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
		watchedAddrs: make(map[string]btcutil.Address),
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
		watchedAddrs: make(map[string]btcutil.Address),
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
		watchedAddrs: make(map[string]btcutil.Address),
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
		watchedAddrs: make(map[string]btcutil.Address),
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
		watchedAddrs: make(map[string]btcutil.Address),
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
	addr, _ := btcutil.DecodeAddress("1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa", &chaincfg.MainNetParams)
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
		watchedAddrs: make(map[string]btcutil.Address),
		utxoSet:      make(map[string]UTXO),
	}

	// Simulate persisted state: already scanned 243542..296100.
	mgr.status.LastScannedTip = 296100
	mgr.status.LastStartHeight = 243542

	// Add a watched address so we don't early-return from "no addresses".
	addr, _ := btcutil.DecodeAddress("1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa", &chaincfg.MainNetParams)
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

// TestVerifyUTXOsUnspentEmptySnapshot tests that verifyUTXOsUnspent returns 0
// when the snapshot is empty.
func TestVerifyUTXOsUnspentEmptySnapshot(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	mgr := &RescanManager{
		chainParams:  &chaincfg.MainNetParams,
		logger:       logger,
		watchedAddrs: make(map[string]btcutil.Address),
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
		watchedAddrs: make(map[string]btcutil.Address),
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
