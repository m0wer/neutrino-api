package neutrino

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/btcsuite/btclog"
)

func newTestStore(t *testing.T) (*StateStore, string) {
	t.Helper()
	dir := t.TempDir()
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	store, err := OpenStateStore(dir, logger)
	if err != nil {
		t.Fatalf("OpenStateStore() error: %v", err)
	}
	return store, dir
}

func TestOpenStateStore(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()

	// Verify the database file was created.
	dbPath := filepath.Join(store.db.Path())
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Errorf("expected database file to exist at %s", dbPath)
	}
}

func TestOpenStateStoreInvalidDir(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	_, err := OpenStateStore("/nonexistent/path/that/cannot/exist", logger)
	if err == nil {
		t.Error("expected error when opening store in invalid directory")
	}
}

func TestStateStoreWatchedAddrs(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()

	// Initially empty.
	addrs, err := store.LoadWatchedAddrs()
	if err != nil {
		t.Fatalf("LoadWatchedAddrs() error: %v", err)
	}
	if len(addrs) != 0 {
		t.Errorf("expected 0 addresses, got %d", len(addrs))
	}

	// Save some addresses.
	testAddrs := []string{
		"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",
		"1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2",
		"bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
	}
	for _, addr := range testAddrs {
		if err := store.SaveWatchedAddr(addr); err != nil {
			t.Fatalf("SaveWatchedAddr(%s) error: %v", addr, err)
		}
	}

	// Reload and verify.
	loaded, err := store.LoadWatchedAddrs()
	if err != nil {
		t.Fatalf("LoadWatchedAddrs() error: %v", err)
	}
	if len(loaded) != len(testAddrs) {
		t.Errorf("expected %d addresses, got %d", len(testAddrs), len(loaded))
	}

	// Check all addresses are present.
	addrSet := make(map[string]bool)
	for _, a := range loaded {
		addrSet[a] = true
	}
	for _, a := range testAddrs {
		if !addrSet[a] {
			t.Errorf("expected address %s to be in loaded set", a)
		}
	}

	// Duplicate save should be idempotent.
	if err := store.SaveWatchedAddr(testAddrs[0]); err != nil {
		t.Fatalf("SaveWatchedAddr() duplicate error: %v", err)
	}
	loaded, _ = store.LoadWatchedAddrs()
	if len(loaded) != len(testAddrs) {
		t.Errorf("expected %d addresses after duplicate save, got %d", len(testAddrs), len(loaded))
	}
}

func TestStateStoreUTXOSet(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()

	// Initially empty.
	utxos, err := store.LoadUTXOSet()
	if err != nil {
		t.Fatalf("LoadUTXOSet() error: %v", err)
	}
	if len(utxos) != 0 {
		t.Errorf("expected 0 UTXOs, got %d", len(utxos))
	}

	// Save a set of UTXOs.
	testUTXOs := map[string]UTXO{
		"tx1:0": {
			TxID:         "tx1",
			Vout:         0,
			Value:        50000000,
			Address:      "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",
			ScriptPubKey: "76a914",
			Height:       100,
		},
		"tx2:1": {
			TxID:         "tx2",
			Vout:         1,
			Value:        25000000,
			Address:      "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2",
			ScriptPubKey: "76a914",
			Height:       200,
		},
	}

	if err := store.SaveUTXOSet(testUTXOs); err != nil {
		t.Fatalf("SaveUTXOSet() error: %v", err)
	}

	// Reload and verify.
	loaded, err := store.LoadUTXOSet()
	if err != nil {
		t.Fatalf("LoadUTXOSet() error: %v", err)
	}
	if len(loaded) != len(testUTXOs) {
		t.Errorf("expected %d UTXOs, got %d", len(testUTXOs), len(loaded))
	}

	for key, expected := range testUTXOs {
		got, ok := loaded[key]
		if !ok {
			t.Errorf("expected UTXO %s to be in loaded set", key)
			continue
		}
		if got.TxID != expected.TxID || got.Vout != expected.Vout || got.Value != expected.Value ||
			got.Address != expected.Address || got.Height != expected.Height {
			t.Errorf("UTXO %s mismatch: got %+v, want %+v", key, got, expected)
		}
	}

	// Overwrite with a different set (atomic replace).
	newUTXOs := map[string]UTXO{
		"tx3:0": {
			TxID:    "tx3",
			Vout:    0,
			Value:   10000000,
			Address: "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",
			Height:  300,
		},
	}
	if err := store.SaveUTXOSet(newUTXOs); err != nil {
		t.Fatalf("SaveUTXOSet() replace error: %v", err)
	}

	loaded, _ = store.LoadUTXOSet()
	if len(loaded) != 1 {
		t.Errorf("expected 1 UTXO after replace, got %d", len(loaded))
	}
	if _, ok := loaded["tx3:0"]; !ok {
		t.Error("expected tx3:0 in replaced set")
	}
	if _, ok := loaded["tx1:0"]; ok {
		t.Error("tx1:0 should have been replaced")
	}
}

func TestStateStoreEmptyUTXOSet(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()

	// Save empty set.
	if err := store.SaveUTXOSet(map[string]UTXO{}); err != nil {
		t.Fatalf("SaveUTXOSet() empty error: %v", err)
	}

	loaded, err := store.LoadUTXOSet()
	if err != nil {
		t.Fatalf("LoadUTXOSet() error: %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("expected 0 UTXOs, got %d", len(loaded))
	}
}

func TestStateStoreRescanMeta(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()

	// Initially zero.
	tip, start, err := store.LoadRescanMeta()
	if err != nil {
		t.Fatalf("LoadRescanMeta() error: %v", err)
	}
	if tip != 0 || start != 0 {
		t.Errorf("expected (0, 0), got (%d, %d)", tip, start)
	}

	// Save metadata.
	if err := store.SaveRescanMeta(296102, 243542); err != nil {
		t.Fatalf("SaveRescanMeta() error: %v", err)
	}

	// Reload and verify.
	tip, start, err = store.LoadRescanMeta()
	if err != nil {
		t.Fatalf("LoadRescanMeta() error: %v", err)
	}
	if tip != 296102 {
		t.Errorf("expected tip 296102, got %d", tip)
	}
	if start != 243542 {
		t.Errorf("expected start 243542, got %d", start)
	}

	// Update metadata.
	if err := store.SaveRescanMeta(296200, 296102); err != nil {
		t.Fatalf("SaveRescanMeta() update error: %v", err)
	}
	tip, start, _ = store.LoadRescanMeta()
	if tip != 296200 {
		t.Errorf("expected updated tip 296200, got %d", tip)
	}
	if start != 296102 {
		t.Errorf("expected updated start 296102, got %d", start)
	}
}

func TestStateStorePersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	backend := btclog.NewBackend(os.Stdout)
	logger := backend.Logger("TEST")

	// Open, write data, close.
	store1, err := OpenStateStore(dir, logger)
	if err != nil {
		t.Fatalf("OpenStateStore() error: %v", err)
	}

	if err := store1.SaveWatchedAddr("1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"); err != nil {
		t.Fatalf("SaveWatchedAddr() error: %v", err)
	}
	testUTXOs := map[string]UTXO{
		"tx1:0": {TxID: "tx1", Vout: 0, Value: 50000000, Address: "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa", Height: 100},
	}
	if err := store1.SaveUTXOSet(testUTXOs); err != nil {
		t.Fatalf("SaveUTXOSet() error: %v", err)
	}
	if err := store1.SaveRescanMeta(296102, 243542); err != nil {
		t.Fatalf("SaveRescanMeta() error: %v", err)
	}
	store1.Close()

	// Reopen and verify all data survived.
	store2, err := OpenStateStore(dir, logger)
	if err != nil {
		t.Fatalf("OpenStateStore() reopen error: %v", err)
	}
	defer store2.Close()

	addrs, _ := store2.LoadWatchedAddrs()
	if len(addrs) != 1 || addrs[0] != "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa" {
		t.Errorf("expected 1 address after reopen, got %v", addrs)
	}

	utxos, _ := store2.LoadUTXOSet()
	if len(utxos) != 1 {
		t.Errorf("expected 1 UTXO after reopen, got %d", len(utxos))
	}
	if utxo, ok := utxos["tx1:0"]; !ok || utxo.Value != 50000000 {
		t.Errorf("UTXO data mismatch after reopen: %+v", utxo)
	}

	tip, start, _ := store2.LoadRescanMeta()
	if tip != 296102 || start != 243542 {
		t.Errorf("metadata mismatch after reopen: tip=%d, start=%d", tip, start)
	}
}

func TestStateStoreCloseNil(t *testing.T) {
	// Closing a store with nil db should not panic.
	s := &StateStore{db: nil, logger: btclog.NewBackend(os.Stdout).Logger("TEST")}
	if err := s.Close(); err != nil {
		t.Errorf("Close() on nil db should not error, got: %v", err)
	}
}
