package neutrino

import (
	"testing"

	"github.com/btcsuite/btcd/btcutil/v2"
	"github.com/btcsuite/btcd/wire/v2"
)

// TestRecordTxHistoryMerge verifies the per-scan history merge logic:
// receive-only, spend-only, and combined receive+spend on one tx.
func TestRecordTxHistoryMerge(t *testing.T) {
	mgr := &RescanManager{logger: newTrackerLogger()}

	msgTx := wire.NewMsgTx(2)
	msgTx.AddTxOut(wire.NewTxOut(1000, []byte{0x00, 0x14}))
	tx := btcutil.NewTx(msgTx)
	txid := tx.Hash().String()

	history := make(map[string]*TxHistoryRecord)

	// First observation: a receive to addrA.
	mgr.recordTxHistory(history, tx, txid, 100, []string{"addrA"}, false)
	rec := history[txid]
	if rec == nil {
		t.Fatal("expected a record")
	}
	if rec.Direction != "receive" || rec.Height != 100 || !rec.Confirmed {
		t.Errorf("unexpected receive record: %+v", rec)
	}
	if rec.Hex == "" {
		t.Error("expected serialized hex")
	}

	// Second observation of the same tx: it also spends a watched outpoint,
	// and pays addrA again (dedup) plus addrB.
	mgr.recordTxHistory(history, tx, txid, 100, []string{"addrA", "addrB"}, true)
	rec = history[txid]
	if rec.Direction != "receive,spend" {
		t.Errorf("expected receive,spend, got %q", rec.Direction)
	}
	if len(rec.Addresses) != 2 {
		t.Errorf("expected deduped 2 addresses, got %v", rec.Addresses)
	}
}

// TestRecordTxHistorySpendOnly verifies a pure spend (no watched outputs).
func TestRecordTxHistorySpendOnly(t *testing.T) {
	mgr := &RescanManager{logger: newTrackerLogger()}
	msgTx := wire.NewMsgTx(2)
	msgTx.AddTxOut(wire.NewTxOut(1000, []byte{0x00, 0x14}))
	tx := btcutil.NewTx(msgTx)
	txid := tx.Hash().String()

	history := make(map[string]*TxHistoryRecord)
	mgr.recordTxHistory(history, tx, txid, 50, nil, true)
	rec := history[txid]
	if rec == nil || rec.Direction != "spend" {
		t.Fatalf("expected spend record, got %+v", rec)
	}
	if len(rec.Addresses) != 0 {
		t.Errorf("expected no addresses for pure spend, got %v", rec.Addresses)
	}
}

// TestTxHistorySinceDisabled verifies the manager returns nothing when history
// tracking is disabled or no store is set.
func TestTxHistorySinceDisabled(t *testing.T) {
	mgr := &RescanManager{logger: newTrackerLogger()}
	mgr.SetTxHistoryEnabled(false)
	got, err := mgr.TxHistorySince(0)
	if err != nil {
		t.Fatalf("TxHistorySince() error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil when disabled, got %v", got)
	}
}
