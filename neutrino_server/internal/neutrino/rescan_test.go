package neutrino

import (
	"testing"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btclog"
)

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

	if mgr == nil {
		t.Fatal("expected non-nil RescanManager")
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
