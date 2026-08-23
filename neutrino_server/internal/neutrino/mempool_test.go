package neutrino

import (
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/btcsuite/btcd/address/v2"
	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/txscript/v2"
	"github.com/btcsuite/btcd/wire/v2"
	"github.com/btcsuite/btclog"
)

// --- Test helpers ---

func newTrackerLogger() btclog.Logger {
	return btclog.NewBackend(io.Discard).Logger("MEMTEST")
}

// newTestRescanMgr creates a RescanManager populated with the given watched
// address and (optionally) confirmed UTXOs. The address is decoded against
// mainnet params (callers pass mainnet addresses).
func newTestRescanMgr(t *testing.T, watchedAddr string, confirmed map[string]UTXO) *RescanManager {
	t.Helper()
	mgr := &RescanManager{
		chainParams:  &chaincfg.MainNetParams,
		logger:       newTrackerLogger(),
		watchedAddrs: make(map[string]address.Address),
		utxoSet:      make(map[string]UTXO),
	}
	if watchedAddr != "" {
		addr, err := address.DecodeAddress(watchedAddr, &chaincfg.MainNetParams)
		if err != nil {
			t.Fatalf("decode watched addr: %v", err)
		}
		mgr.watchedAddrs[watchedAddr] = addr
	}
	for k, v := range confirmed {
		mgr.utxoSet[k] = v
	}
	return mgr
}

// newTracker builds a tracker bound to the given rescan manager. The tracker
// has no chain service or peers — it's only used to exercise handleTx and
// the in-memory state machine.
func newTracker(rescanMgr *RescanManager) *MempoolTracker {
	return &MempoolTracker{
		rescanMgr: rescanMgr,
		logger:    newTrackerLogger(),
		entries:   make(map[string]*mempoolEntry),
		utxos:     make(map[string]MempoolUTXO),
		spends:    make(map[string]MempoolSpend),
		fetched:   make(map[chainhash.Hash]int64),
		inflight:  make(map[chainhash.Hash]int64),
		peers:     make(map[string]func()),
		quit:      make(chan struct{}),
		done:      make(chan struct{}),
	}
}

// pkScriptFor returns the standard pkScript for a mainnet address.
func pkScriptFor(t *testing.T, addrStr string) []byte {
	t.Helper()
	addr, err := address.DecodeAddress(addrStr, &chaincfg.MainNetParams)
	if err != nil {
		t.Fatalf("decode addr: %v", err)
	}
	script, err := txscript.PayToAddrScript(addr)
	if err != nil {
		t.Fatalf("payToAddrScript: %v", err)
	}
	return script
}

// txWithOutput builds a tx that has a single output to `pkScript` with the
// given value. No inputs are set unless `prevOut` is non-nil.
func txWithOutput(pkScript []byte, value int64, prevOut *wire.OutPoint) *wire.MsgTx {
	tx := wire.NewMsgTx(2)
	if prevOut != nil {
		tx.AddTxIn(&wire.TxIn{PreviousOutPoint: *prevOut, Sequence: 0xffffffff})
	}
	tx.AddTxOut(wire.NewTxOut(value, pkScript))
	return tx
}

func mustHash(t *testing.T, hexStr string) chainhash.Hash {
	t.Helper()
	h, err := chainhash.NewHashFromStr(hexStr)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	return *h
}

// --- Tests ---

const watchedAddrMain = "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"

// TestHandleTx_TracksOutputToWatchedAddress verifies that an unconfirmed tx
// paying a watched address creates a tracked MempoolUTXO.
func TestHandleTx_TracksOutputToWatchedAddress(t *testing.T) {
	mgr := newTestRescanMgr(t, watchedAddrMain, nil)
	tr := newTracker(mgr)

	pk := pkScriptFor(t, watchedAddrMain)
	tx := txWithOutput(pk, 12345, nil)

	tr.handleTx(tx)

	stats := tr.Stats()
	if stats.Entries != 1 || stats.UTXOs != 1 {
		t.Fatalf("expected 1 entry / 1 utxo, got %+v", stats)
	}
	utxos := tr.MempoolUTXOsForAddresses([]string{watchedAddrMain})
	if len(utxos) != 1 {
		t.Fatalf("expected 1 mempool utxo, got %d", len(utxos))
	}
	if utxos[0].Value != 12345 || utxos[0].Address != watchedAddrMain {
		t.Errorf("unexpected utxo: %+v", utxos[0])
	}
	if utxos[0].FirstSeen == 0 {
		t.Error("FirstSeen should be set")
	}
}

// TestHandleTx_IgnoresUnwatchedOutputs verifies that a tx whose outputs do
// not pay any watched address is dropped.
func TestHandleTx_IgnoresUnwatchedOutputs(t *testing.T) {
	mgr := newTestRescanMgr(t, watchedAddrMain, nil)
	tr := newTracker(mgr)

	// Pay to a different mainnet address.
	otherAddr := "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2"
	pk := pkScriptFor(t, otherAddr)
	tx := txWithOutput(pk, 9999, nil)

	tr.handleTx(tx)

	stats := tr.Stats()
	if stats.Entries != 0 || stats.UTXOs != 0 {
		t.Fatalf("expected no tracking, got %+v", stats)
	}
}

// TestHandleTx_DetectsSpendOfWatchedConfirmedUTXO verifies that a tx spending
// a confirmed UTXO tracked by the rescan manager is recorded as a mempool
// spend.
func TestHandleTx_DetectsSpendOfWatchedConfirmedUTXO(t *testing.T) {
	prevTxidStr := "0000000000000000000000000000000000000000000000000000000000000001"
	confirmed := map[string]UTXO{
		prevTxidStr + ":0": {
			TxID: prevTxidStr, Vout: 0, Value: 5000,
			Address: watchedAddrMain, Height: 800000,
		},
	}
	mgr := newTestRescanMgr(t, watchedAddrMain, confirmed)
	tr := newTracker(mgr)

	prevHash := mustHash(t, prevTxidStr)
	prevOut := wire.OutPoint{Hash: prevHash, Index: 0}
	pk := pkScriptFor(t, watchedAddrMain)
	tx := txWithOutput(pk, 4000, &prevOut)

	tr.handleTx(tx)

	stats := tr.Stats()
	if stats.Spends != 1 {
		t.Fatalf("expected 1 mempool spend, got %+v", stats)
	}
	sp, ok := tr.MempoolSpendOf(prevTxidStr, 0)
	if !ok {
		t.Fatal("expected MempoolSpendOf to return spend")
	}
	if sp.SpendingTxID != tx.TxHash().String() || sp.InputIndex != 0 {
		t.Errorf("unexpected spend: %+v", sp)
	}
}

// TestHandleTx_DuplicateTxIgnored verifies that delivering the same tx twice
// only records it once (peers can announce duplicates).
func TestHandleTx_DuplicateTxIgnored(t *testing.T) {
	mgr := newTestRescanMgr(t, watchedAddrMain, nil)
	tr := newTracker(mgr)
	pk := pkScriptFor(t, watchedAddrMain)
	tx := txWithOutput(pk, 1000, nil)

	tr.handleTx(tx)
	tr.handleTx(tx)

	stats := tr.Stats()
	if stats.Entries != 1 || stats.UTXOs != 1 {
		t.Fatalf("dedupe failed: %+v", stats)
	}
}

// TestHandleTx_RBFReplacesPriorEntry verifies that when two distinct txs
// spend the same watched outpoint, the second one evicts the first (RBF).
func TestHandleTx_RBFReplacesPriorEntry(t *testing.T) {
	prevTxidStr := "0000000000000000000000000000000000000000000000000000000000000002"
	confirmed := map[string]UTXO{
		prevTxidStr + ":0": {
			TxID: prevTxidStr, Vout: 0, Value: 5000,
			Address: watchedAddrMain, Height: 800000,
		},
	}
	mgr := newTestRescanMgr(t, watchedAddrMain, confirmed)
	tr := newTracker(mgr)

	prevHash := mustHash(t, prevTxidStr)
	prevOut := wire.OutPoint{Hash: prevHash, Index: 0}
	pk := pkScriptFor(t, watchedAddrMain)

	// First tx pays 4000 to watched addr.
	tx1 := txWithOutput(pk, 4000, &prevOut)
	tr.handleTx(tx1)

	// Replacement pays 4500 to watched addr (different output → different txid).
	tx2 := txWithOutput(pk, 4500, &prevOut)

	if tx1.TxHash() == tx2.TxHash() {
		t.Fatal("test setup: txids unexpectedly equal")
	}
	tr.handleTx(tx2)

	stats := tr.Stats()
	if stats.Entries != 1 {
		t.Fatalf("expected 1 entry after RBF, got %+v", stats)
	}
	if _, ok := tr.MempoolTxByID(tx1.TxHash().String()); ok {
		t.Error("tx1 should have been evicted by RBF")
	}
	if _, ok := tr.MempoolTxByID(tx2.TxHash().String()); !ok {
		t.Error("tx2 should be tracked")
	}
	sp, ok := tr.MempoolSpendOf(prevTxidStr, 0)
	if !ok || sp.SpendingTxID != tx2.TxHash().String() {
		t.Errorf("spend record not updated to replacement: %+v", sp)
	}
}

// TestEvictConfirmed_DropsConfirmedTxs verifies that EvictConfirmed removes
// tracked entries whose txids have confirmed on-chain.
func TestEvictConfirmed_DropsConfirmedTxs(t *testing.T) {
	mgr := newTestRescanMgr(t, watchedAddrMain, nil)
	tr := newTracker(mgr)
	pk := pkScriptFor(t, watchedAddrMain)
	tx := txWithOutput(pk, 1000, nil)
	tr.handleTx(tx)

	tr.EvictConfirmed([]string{tx.TxHash().String()})

	if s := tr.Stats(); s.Entries != 0 || s.UTXOs != 0 {
		t.Fatalf("expected eviction, got %+v", s)
	}
}

// TestEvictAll_ClearsState verifies EvictAll wipes entries, utxos, and spends.
func TestEvictAll_ClearsState(t *testing.T) {
	mgr := newTestRescanMgr(t, watchedAddrMain, nil)
	tr := newTracker(mgr)
	pk := pkScriptFor(t, watchedAddrMain)
	tr.handleTx(txWithOutput(pk, 1000, nil))
	tr.handleTx(txWithOutput(pk, 2000, nil))

	tr.EvictAll()

	if s := tr.Stats(); s.Entries != 0 || s.UTXOs != 0 || s.Spends != 0 {
		t.Fatalf("expected empty state, got %+v", s)
	}
}

// TestEvictExpired_RemovesStaleEntries verifies that the TTL sweep drops
// entries older than mempoolEntryTTL while keeping fresh ones.
func TestEvictExpired_RemovesStaleEntries(t *testing.T) {
	mgr := newTestRescanMgr(t, watchedAddrMain, nil)
	tr := newTracker(mgr)
	pk := pkScriptFor(t, watchedAddrMain)

	stale := txWithOutput(pk, 1000, nil)
	tr.handleTx(stale)
	// Backdate the entry past the TTL window.
	tr.mu.Lock()
	tr.entries[stale.TxHash().String()].firstSeen = time.Now().Add(-mempoolEntryTTL - time.Hour)
	tr.mu.Unlock()

	fresh := txWithOutput(pk, 2000, nil)
	tr.handleTx(fresh)

	tr.evictExpired()

	if _, ok := tr.MempoolTxByID(stale.TxHash().String()); ok {
		t.Error("stale entry should have been evicted")
	}
	if _, ok := tr.MempoolTxByID(fresh.TxHash().String()); !ok {
		t.Error("fresh entry should still be tracked")
	}
}

// TestMempoolUTXOsForAddresses_FiltersByAddress verifies the address filter.
func TestMempoolUTXOsForAddresses_FiltersByAddress(t *testing.T) {
	mgr := newTestRescanMgr(t, watchedAddrMain, nil)
	tr := newTracker(mgr)
	pk := pkScriptFor(t, watchedAddrMain)
	tr.handleTx(txWithOutput(pk, 1000, nil))

	// Empty address slice returns nil.
	if got := tr.MempoolUTXOsForAddresses(nil); got != nil {
		t.Errorf("expected nil for empty addrs, got %v", got)
	}

	// Unknown address returns empty slice (not nil).
	got := tr.MempoolUTXOsForAddresses([]string{"1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2"})
	if len(got) != 0 {
		t.Errorf("expected 0 utxos for unwatched addr, got %d", len(got))
	}

	// Known address returns the entry.
	got = tr.MempoolUTXOsForAddresses([]string{watchedAddrMain})
	if len(got) != 1 {
		t.Errorf("expected 1 utxo, got %d", len(got))
	}
}

// TestHandleTx_NoWatchedScripts_NoOp verifies that with zero watched scripts
// the tracker drops the tx without recording anything.
func TestHandleTx_NoWatchedScripts_NoOp(t *testing.T) {
	mgr := newTestRescanMgr(t, "", nil)
	tr := newTracker(mgr)
	pk := pkScriptFor(t, watchedAddrMain)
	tr.handleTx(txWithOutput(pk, 1000, nil))

	if s := tr.Stats(); s.Entries != 0 {
		t.Fatalf("expected no tracking with zero watched scripts, got %+v", s)
	}
}

// TestHandleTx_ConcurrentSafe is a smoke check for the mu-guarded state
// machine: concurrent handleTx calls must not race or panic.
func TestHandleTx_ConcurrentSafe(t *testing.T) {
	mgr := newTestRescanMgr(t, watchedAddrMain, nil)
	tr := newTracker(mgr)
	pk := pkScriptFor(t, watchedAddrMain)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(v int64) {
			defer wg.Done()
			tx := txWithOutput(pk, v, nil)
			tr.handleTx(tx)
		}(int64(1000 + i))
	}
	wg.Wait()

	if s := tr.Stats(); s.Entries != 50 || s.UTXOs != 50 {
		t.Fatalf("expected 50 entries, got %+v", s)
	}
}

// TestNotifyMempoolConfirmed_OnlyDropsConfirmedTxs is a regression test for
// the post-scan eviction hook: it must drop the txids that were just mined
// without wiping unrelated tracked entries (mempool peers do not reliably
// re-announce already-acked txs, so an over-eager wipe would silently lose
// state until each entry re-broadcasts or confirms).
func TestNotifyMempoolConfirmed_OnlyDropsConfirmedTxs(t *testing.T) {
	mgr := newTestRescanMgr(t, watchedAddrMain, nil)
	tr := newTracker(mgr)
	mgr.SetMempoolTracker(tr)

	pk := pkScriptFor(t, watchedAddrMain)
	mined := txWithOutput(pk, 1111, nil)
	survivor := txWithOutput(pk, 2222, nil)
	tr.handleTx(mined)
	tr.handleTx(survivor)

	// Simulate a successful rescan that downloaded a block containing the
	// `mined` tx plus unrelated traffic; only `mined` should be evicted.
	mgr.notifyMempoolConfirmed([]string{
		mined.TxHash().String(),
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
	})

	if _, ok := tr.MempoolTxByID(mined.TxHash().String()); ok {
		t.Error("mined tx should have been evicted")
	}
	if _, ok := tr.MempoolTxByID(survivor.TxHash().String()); !ok {
		t.Error("survivor tx should still be tracked (only mined tx confirmed)")
	}
	if s := tr.Stats(); s.Entries != 1 {
		t.Fatalf("expected 1 surviving entry, got %+v", s)
	}
}

// TestNotifyMempoolConfirmed_EmptyTxidsIsNoOp documents that an empty
// confirmed-txid slice (e.g. a rescan pass that found no matched blocks)
// leaves the tracker untouched.
func TestNotifyMempoolConfirmed_EmptyTxidsIsNoOp(t *testing.T) {
	mgr := newTestRescanMgr(t, watchedAddrMain, nil)
	tr := newTracker(mgr)
	mgr.SetMempoolTracker(tr)
	pk := pkScriptFor(t, watchedAddrMain)
	tr.handleTx(txWithOutput(pk, 4242, nil))

	mgr.notifyMempoolConfirmed(nil)

	if s := tr.Stats(); s.Entries != 1 {
		t.Fatalf("expected entry to survive empty notify, got %+v", s)
	}
}

// fakePeer is a minimal query.Peer used to assert handleInv's getdata
// routing. It records every QueueMessageWithEncoding invocation; the
// channels and Addr exist only so the type satisfies the interface.
type fakePeer struct {
	addr     string
	sent     []wire.Message
	disconCh chan struct{}
	mu       sync.Mutex
}

func newFakePeer(addr string) *fakePeer {
	return &fakePeer{addr: addr, disconCh: make(chan struct{})}
}

func (p *fakePeer) Addr() string                  { return p.addr }
func (p *fakePeer) OnDisconnect() <-chan struct{} { return p.disconCh }
func (p *fakePeer) SubscribeRecvMsg() (<-chan wire.Message, func()) {
	ch := make(chan wire.Message)
	return ch, func() { close(ch) }
}
func (p *fakePeer) QueueMessageWithEncoding(m wire.Message, _ chan<- struct{},
	_ wire.MessageEncoding,
) {
	p.mu.Lock()
	p.sent = append(p.sent, m)
	p.mu.Unlock()
}

// TestHandleInv_GetDataGoesOnlyToAnnouncingPeer locks in the privacy
// contract documented at the top of mempool.go: getdata for a tx inv is
// issued only to the peer that announced the tx, never broadcast. If we
// fanned out we would leak nothing about *which* txs we care about, but we
// would still expand bandwidth quadratically and give a network-wide
// observer extra fingerprintable behaviour.
func TestHandleInv_GetDataGoesOnlyToAnnouncingPeer(t *testing.T) {
	mgr := newTestRescanMgr(t, watchedAddrMain, nil)
	tr := newTracker(mgr)

	announcer := newFakePeer("announcer:8333")
	other := newFakePeer("other:8333")

	inv := wire.NewMsgInv()
	hash := mustHash(t, "0000000000000000000000000000000000000000000000000000000000000ccc")
	if err := inv.AddInvVect(&wire.InvVect{Type: wire.InvTypeTx, Hash: hash}); err != nil {
		t.Fatalf("AddInvVect: %v", err)
	}

	tr.handleInv(announcer, inv)

	if got := len(announcer.sent); got != 1 {
		t.Fatalf("announcer should have received 1 getdata, got %d", got)
	}
	if got := len(other.sent); got != 0 {
		t.Fatalf("non-announcing peer must not receive getdata, got %d", got)
	}
	gd, ok := announcer.sent[0].(*wire.MsgGetData)
	if !ok {
		t.Fatalf("expected MsgGetData, got %T", announcer.sent[0])
	}
	if len(gd.InvList) != 1 || gd.InvList[0].Hash != hash {
		t.Errorf("unexpected getdata payload: %+v", gd.InvList)
	}
	// Witness-stripped form is required so we receive the full tx.
	if gd.InvList[0].Type != wire.InvTypeWitnessTx {
		t.Errorf("expected witness-tx inv type, got %v", gd.InvList[0].Type)
	}
}

// TestHandleInv_NoWatchedAddressesSkipsGetData verifies the early-exit in
// handleInv: when zero addresses are watched, no getdata is issued (no
// point fetching txs we can't match).
func TestHandleInv_NoWatchedAddressesSkipsGetData(t *testing.T) {
	mgr := newTestRescanMgr(t, "", nil)
	tr := newTracker(mgr)

	announcer := newFakePeer("announcer:8333")
	inv := wire.NewMsgInv()
	hash := mustHash(t, "0000000000000000000000000000000000000000000000000000000000000ddd")
	if err := inv.AddInvVect(&wire.InvVect{Type: wire.InvTypeTx, Hash: hash}); err != nil {
		t.Fatalf("AddInvVect: %v", err)
	}

	tr.handleInv(announcer, inv)

	if got := len(announcer.sent); got != 0 {
		t.Fatalf("no getdata expected with zero watched addrs, got %d", got)
	}
}

// TestEvictExpired_DropsStaleInflight verifies the TTL sweep also clears
// abandoned getdata requests so a never-delivered tx (e.g. announcing peer
// disconnected before responding) stops blocking re-fetch from other peers.
func TestEvictExpired_DropsStaleInflight(t *testing.T) {
	mgr := newTestRescanMgr(t, watchedAddrMain, nil)
	tr := newTracker(mgr)

	staleHash := mustHash(t, "0000000000000000000000000000000000000000000000000000000000000aaa")
	freshHash := mustHash(t, "0000000000000000000000000000000000000000000000000000000000000bbb")

	tr.mu.Lock()
	tr.inflight[staleHash] = time.Now().Add(-mempoolInflightTTL - time.Second).UnixNano()
	tr.inflight[freshHash] = time.Now().UnixNano()
	tr.mu.Unlock()

	tr.evictExpired()

	tr.mu.RLock()
	_, staleStillBusy := tr.inflight[staleHash]
	_, freshStillBusy := tr.inflight[freshHash]
	tr.mu.RUnlock()

	if staleStillBusy {
		t.Error("stale inflight entry should have been swept")
	}
	if !freshStillBusy {
		t.Error("fresh inflight entry should still be tracked")
	}
}

// Compile-time guard: ensure mempoolEntry exposes the field handleTx uses.
var _ = func() *mempoolEntry { return &mempoolEntry{firstSeen: time.Now()} }

// Suppress unused-import lint if any helper above is removed.
var _ = fmt.Sprintf
