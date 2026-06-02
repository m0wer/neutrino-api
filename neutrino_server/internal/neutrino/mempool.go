/*
Package neutrino: MempoolTracker keeps a watched-only mempool view by
subscribing to every connected peer's incoming P2P messages, fetching
each announced transaction, and matching it against the watched script
set / confirmed UTXO set tracked by RescanManager.

Privacy model: for every tx inv received from a peer we issue a getdata
back to that same peer (not to all peers) without filtering on any local
heuristic. Because the request set is "every announced tx", peers cannot
infer which addresses or scripts we care about from our fetch pattern.
The bandwidth cost is one extra tx download per inv on top of a normal
SPV flow.

Eviction:
  - A tx is removed when the block-connect hook (RescanManager.runAutoSyncPass)
    confirms it (mempool entries with txids found on-chain are dropped after
    the rescan pass finishes).
  - A tx is dropped when a conflicting tx is observed (RBF: both spend the
    same outpoint we're tracking).
  - A TTL of mempoolEntryTTL sweeps stale entries.
*/
package neutrino

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btclog"
	"github.com/lightninglabs/neutrino"
	"github.com/lightninglabs/neutrino/query"
)

const (
	// mempoolEntryTTL caps how long a watched mempool tx stays in memory
	// without confirmation before being swept. Bitcoin Core defaults are
	// 14 days; we mirror that.
	mempoolEntryTTL = 14 * 24 * time.Hour

	// mempoolEvictionInterval is how often the eviction goroutine runs.
	mempoolEvictionInterval = 30 * time.Minute

	// mempoolFetchedCacheCap limits how many recently-seen txids we
	// remember to avoid re-fetching duplicate invs across peers.
	mempoolFetchedCacheCap = 50000

	// mempoolInflightTTL bounds how long a getdata request stays in the
	// inflight set without the tx actually arriving. When a peer
	// disconnects or simply never responds, the entry would otherwise
	// pin the txid in `inflight` forever and prevent any other peer's
	// inv from triggering a re-fetch. 60s is generous: a real tx
	// delivery completes in well under one second on a healthy link.
	mempoolInflightTTL = 60 * time.Second
)

// MempoolUTXO represents an unconfirmed UTXO sourced from a watched mempool
// transaction. It mirrors UTXO except that Height is always 0 and FirstSeen
// records when the tracker first observed the parent transaction.
type MempoolUTXO struct {
	TxID         string `json:"txid"`
	Vout         uint32 `json:"vout"`
	Value        int64  `json:"value"`
	Address      string `json:"address"`
	ScriptPubKey string `json:"scriptpubkey"`
	FirstSeen    int64  `json:"first_seen"`
}

// MempoolSpend records that a watched outpoint has been spent by an
// unconfirmed transaction.
type MempoolSpend struct {
	SpendingTxID string `json:"spending_txid"`
	InputIndex   uint32 `json:"input_index"`
	FirstSeen    int64  `json:"first_seen"`
}

// mempoolEntry is the internal representation of a tracked mempool tx.
type mempoolEntry struct {
	tx        *wire.MsgTx
	txid      string
	firstSeen time.Time
	// outputs we created that match watched addresses (key "txid:vout").
	createdUTXOs []string
	// confirmed outpoints this tx spends (key "txid:vout").
	spentOutpoints []string
}

// MempoolTracker holds the watched-only mempool view.
type MempoolTracker struct {
	chainService *neutrino.ChainService
	rescanMgr    *RescanManager
	logger       btclog.Logger

	mu       sync.RWMutex
	entries  map[string]*mempoolEntry // txid -> entry
	utxos    map[string]MempoolUTXO   // "txid:vout" -> mempool utxo
	spends   map[string]MempoolSpend  // "prevTxid:prevVout" -> spend record
	fetched  map[chainhash.Hash]int64 // txid -> unix nanos when first seen-as-inv
	inflight map[chainhash.Hash]int64 // txid -> unix nanos when getdata issued

	// peers is the set of peers we're currently subscribed to. Cancel
	// closures are stored so we can unsubscribe on shutdown / disconnect.
	peers map[string]func()

	quit chan struct{}
	done chan struct{}
}

// NewMempoolTracker constructs a tracker. It must be started via Start.
func NewMempoolTracker(cs *neutrino.ChainService, rescanMgr *RescanManager,
	logger btclog.Logger) *MempoolTracker {

	return &MempoolTracker{
		chainService: cs,
		rescanMgr:    rescanMgr,
		logger:       logger,
		entries:      make(map[string]*mempoolEntry),
		utxos:        make(map[string]MempoolUTXO),
		spends:       make(map[string]MempoolSpend),
		fetched:      make(map[chainhash.Hash]int64),
		inflight:     make(map[chainhash.Hash]int64),
		peers:        make(map[string]func()),
		quit:         make(chan struct{}),
		done:         make(chan struct{}),
	}
}

// Start launches the tracker's main loop. The loop subscribes to
// ChainService.ConnectedPeers and per-peer SubscribeRecvMsg until Stop
// is called.
func (m *MempoolTracker) Start(ctx context.Context) error {
	if m.chainService == nil {
		return fmt.Errorf("mempool tracker: chain service is nil")
	}
	if m.rescanMgr == nil {
		return fmt.Errorf("mempool tracker: rescan manager is nil")
	}

	go m.run(ctx)
	go m.evictionLoop()
	return nil
}

// Stop signals the tracker to shut down and waits for the main loop to exit.
func (m *MempoolTracker) Stop() {
	select {
	case <-m.quit:
		return
	default:
		close(m.quit)
	}
	<-m.done
}

func (m *MempoolTracker) run(ctx context.Context) {
	defer close(m.done)

	m.logger.Info("Mempool tracker: starting")

	peerCh, cancelPeerSub, err := m.chainService.ConnectedPeers()
	if err != nil {
		m.logger.Errorf("Mempool tracker: failed to subscribe to peers: %v", err)
		return
	}
	defer cancelPeerSub()

	for {
		select {
		case <-m.quit:
			m.logger.Info("Mempool tracker: stopping")
			m.disconnectAllPeers()
			return
		case <-ctx.Done():
			m.logger.Info("Mempool tracker: context cancelled")
			m.disconnectAllPeers()
			return
		case peer, ok := <-peerCh:
			if !ok {
				m.logger.Warn("Mempool tracker: connected-peers channel closed")
				return
			}
			m.handleNewPeer(peer)
		}
	}
}

func (m *MempoolTracker) handleNewPeer(peer query.Peer) {
	addr := peer.Addr()

	m.mu.Lock()
	if _, exists := m.peers[addr]; exists {
		m.mu.Unlock()
		return
	}

	msgCh, cancelSub := peer.SubscribeRecvMsg()
	m.peers[addr] = cancelSub
	m.mu.Unlock()

	m.logger.Debugf("Mempool tracker: subscribed to peer %s", addr)
	go m.peerRecvLoop(peer, msgCh)
}

func (m *MempoolTracker) peerRecvLoop(peer query.Peer, msgCh <-chan wire.Message) {
	addr := peer.Addr()
	defer m.unsubscribePeer(addr)

	disconnectCh := peer.OnDisconnect()
	for {
		select {
		case <-m.quit:
			return
		case <-disconnectCh:
			m.logger.Debugf("Mempool tracker: peer %s disconnected", addr)
			return
		case msg, ok := <-msgCh:
			if !ok {
				return
			}
			m.handlePeerMessage(peer, msg)
		}
	}
}

func (m *MempoolTracker) unsubscribePeer(addr string) {
	m.mu.Lock()
	cancel, ok := m.peers[addr]
	delete(m.peers, addr)
	m.mu.Unlock()
	if ok && cancel != nil {
		cancel()
	}
}

func (m *MempoolTracker) disconnectAllPeers() {
	m.mu.Lock()
	cancels := make([]func(), 0, len(m.peers))
	for _, c := range m.peers {
		cancels = append(cancels, c)
	}
	m.peers = make(map[string]func())
	m.mu.Unlock()

	for _, c := range cancels {
		if c != nil {
			c()
		}
	}
}

func (m *MempoolTracker) handlePeerMessage(peer query.Peer, msg wire.Message) {
	switch v := msg.(type) {
	case *wire.MsgInv:
		m.handleInv(peer, v)
	case *wire.MsgTx:
		m.handleTx(v)
	}
}

// handleInv collects MSG_TX (and MSG_WITNESS_TX) hashes from an inv message
// and issues a single getdata to the announcing peer for any txids we have
// not already fetched.
func (m *MempoolTracker) handleInv(peer query.Peer, inv *wire.MsgInv) {
	if len(inv.InvList) == 0 {
		return
	}

	// Skip work entirely when there is nothing to match against.
	if !m.rescanMgr.HasWatchedAddresses() {
		return
	}

	getData := wire.NewMsgGetData()
	now := time.Now().UnixNano()

	m.mu.Lock()
	for _, iv := range inv.InvList {
		if iv.Type != wire.InvTypeTx && iv.Type != wire.InvTypeWitnessTx {
			continue
		}
		if _, seen := m.fetched[iv.Hash]; seen {
			continue
		}
		if started, busy := m.inflight[iv.Hash]; busy {
			// Allow re-fetch if the previous attempt is older than
			// the TTL: the peer that received the original getdata
			// may have disconnected or silently dropped it.
			if now-started < mempoolInflightTTL.Nanoseconds() {
				continue
			}
		}

		m.fetched[iv.Hash] = now
		m.inflight[iv.Hash] = now

		// Always request the witness-stripped form via the witness inv
		// type so we get the full tx.
		_ = getData.AddInvVect(&wire.InvVect{
			Type: wire.InvTypeWitnessTx,
			Hash: iv.Hash,
		})
	}

	// Bound the dedupe cache.
	if len(m.fetched) > mempoolFetchedCacheCap {
		m.trimFetchedCacheLocked()
	}
	m.mu.Unlock()

	if len(getData.InvList) == 0 {
		return
	}

	m.logger.Debugf("Mempool tracker: requesting %d txs from peer %s",
		len(getData.InvList), peer.Addr())
	peer.QueueMessageWithEncoding(getData, nil, wire.WitnessEncoding)
}

// trimFetchedCacheLocked drops the oldest half of the fetched cache. Caller
// must hold m.mu.
func (m *MempoolTracker) trimFetchedCacheLocked() {
	// Cheap eviction: just clear it. Re-fetching a few duplicates is fine
	// and avoids per-entry timestamp accounting on the hot path.
	m.fetched = make(map[chainhash.Hash]int64, mempoolFetchedCacheCap/2)
}

// handleTx evaluates an incoming transaction against the watched script set
// and confirmed UTXO set. Matching transactions are stored as mempool
// entries and surface as MempoolUTXO / MempoolSpend records.
func (m *MempoolTracker) handleTx(tx *wire.MsgTx) {
	txHash := tx.TxHash()

	m.mu.Lock()
	delete(m.inflight, txHash)
	m.mu.Unlock()

	txid := txHash.String()

	// Build per-pass snapshot of watched scripts to minimise lock churn.
	scriptIndex := m.rescanMgr.WatchedScriptIndex()
	if len(scriptIndex) == 0 {
		return
	}

	createdUTXOs := make([]MempoolUTXO, 0)
	now := time.Now().Unix()
	for vout, txOut := range tx.TxOut {
		scriptHex := hex.EncodeToString(txOut.PkScript)
		addrStr, ok := scriptIndex[scriptHex]
		if !ok {
			continue
		}
		createdUTXOs = append(createdUTXOs, MempoolUTXO{
			TxID:         txid,
			Vout:         uint32(vout),
			Value:        txOut.Value,
			Address:      addrStr,
			ScriptPubKey: scriptHex,
			FirstSeen:    now,
		})
	}

	spentOutpoints := make([]MempoolSpend, 0)
	spentKeys := make([]string, 0)
	for inputIdx, txIn := range tx.TxIn {
		prevOut := txIn.PreviousOutPoint
		key := fmt.Sprintf("%s:%d", prevOut.Hash.String(), prevOut.Index)

		if _, watched := m.rescanMgr.LookupConfirmedUTXO(
			prevOut.Hash.String(), prevOut.Index,
		); !watched {
			// Also count it if it's a chain we've already seen in
			// the mempool: a watched-output -> mempool-spend chain.
			m.mu.RLock()
			_, watchedMem := m.utxos[key]
			m.mu.RUnlock()
			if !watchedMem {
				continue
			}
		}

		spentOutpoints = append(spentOutpoints, MempoolSpend{
			SpendingTxID: txid,
			InputIndex:   uint32(inputIdx),
			FirstSeen:    now,
		})
		spentKeys = append(spentKeys, key)
	}

	if len(createdUTXOs) == 0 && len(spentOutpoints) == 0 {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Skip if we've already recorded this tx (duplicate from another peer).
	if _, exists := m.entries[txid]; exists {
		return
	}

	entry := &mempoolEntry{
		tx:             tx,
		txid:           txid,
		firstSeen:      time.Now(),
		createdUTXOs:   make([]string, 0, len(createdUTXOs)),
		spentOutpoints: spentKeys,
	}

	for _, u := range createdUTXOs {
		key := fmt.Sprintf("%s:%d", u.TxID, u.Vout)
		m.utxos[key] = u
		entry.createdUTXOs = append(entry.createdUTXOs, key)
	}
	for i, sp := range spentOutpoints {
		// RBF: if a different tx already spends the same outpoint,
		// drop the older entry first.
		if existing, ok := m.spends[spentKeys[i]]; ok &&
			existing.SpendingTxID != txid {
			m.dropEntryLocked(existing.SpendingTxID, "rbf-replaced")
		}
		m.spends[spentKeys[i]] = sp
	}
	m.entries[txid] = entry

	m.logger.Infof(
		"Mempool tracker: tracked tx %s (created=%d, spent=%d)",
		txid, len(createdUTXOs), len(spentOutpoints),
	)
}

// dropEntryLocked removes a tracked tx and any UTXOs/spends it produced.
// Caller must hold m.mu (write lock).
func (m *MempoolTracker) dropEntryLocked(txid string, reason string) {
	entry, ok := m.entries[txid]
	if !ok {
		return
	}

	for _, key := range entry.createdUTXOs {
		delete(m.utxos, key)
	}
	for _, key := range entry.spentOutpoints {
		// Only delete the spend record if it still points at this txid.
		if sp, exists := m.spends[key]; exists && sp.SpendingTxID == txid {
			delete(m.spends, key)
		}
	}
	delete(m.entries, txid)

	m.logger.Debugf("Mempool tracker: dropped tx %s (%s)", txid, reason)
}

// EvictConfirmed is called by the auto-sync pass after each rescan to drop
// any mempool entries that have now been confirmed on-chain. The caller
// passes the set of txids confirmed since the last call.
func (m *MempoolTracker) EvictConfirmed(confirmedTxids []string) {
	if len(confirmedTxids) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, txid := range confirmedTxids {
		if _, ok := m.entries[txid]; ok {
			m.dropEntryLocked(txid, "confirmed")
		}
	}
}

// EvictAll clears every tracked entry. Useful on reorg.
func (m *MempoolTracker) EvictAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = make(map[string]*mempoolEntry)
	m.utxos = make(map[string]MempoolUTXO)
	m.spends = make(map[string]MempoolSpend)
}

func (m *MempoolTracker) evictionLoop() {
	ticker := time.NewTicker(mempoolEvictionInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.quit:
			return
		case <-ticker.C:
			m.evictExpired()
		}
	}
}

func (m *MempoolTracker) evictExpired() {
	cutoff := time.Now().Add(-mempoolEntryTTL)
	inflightCutoff := time.Now().Add(-mempoolInflightTTL).UnixNano()
	m.mu.Lock()
	defer m.mu.Unlock()

	for txid, entry := range m.entries {
		if entry.firstSeen.Before(cutoff) {
			m.dropEntryLocked(txid, "ttl-expired")
		}
	}
	// Sweep abandoned getdata requests so a never-delivered tx (peer
	// disconnect, dropped message) stops blocking re-fetch from other
	// peers.
	for hash, started := range m.inflight {
		if started < inflightCutoff {
			delete(m.inflight, hash)
		}
	}
}

// MempoolUTXOsForAddresses returns every tracked unconfirmed UTXO whose
// output address is in the requested set. Order is unspecified.
func (m *MempoolTracker) MempoolUTXOsForAddresses(addresses []string) []MempoolUTXO {
	if len(addresses) == 0 {
		return nil
	}
	want := make(map[string]struct{}, len(addresses))
	for _, a := range addresses {
		want[a] = struct{}{}
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]MempoolUTXO, 0)
	for _, u := range m.utxos {
		if _, ok := want[u.Address]; ok {
			out = append(out, u)
		}
	}
	return out
}

// MempoolSpendOf returns whether the outpoint has an unconfirmed spend in the
// tracked mempool, and if so, the recorded spend.
func (m *MempoolTracker) MempoolSpendOf(txid string, vout uint32) (MempoolSpend, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sp, ok := m.spends[fmt.Sprintf("%s:%d", txid, vout)]
	return sp, ok
}

// MempoolTxByID returns the cached unconfirmed transaction by txid, if it is
// being tracked.
func (m *MempoolTracker) MempoolTxByID(txid string) (*wire.MsgTx, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.entries[txid]
	if !ok {
		return nil, false
	}
	return entry.tx, true
}

// Stats returns current tracker counts. Useful for /v1/status surfacing.
type MempoolStats struct {
	Entries int `json:"entries"`
	UTXOs   int `json:"utxos"`
	Spends  int `json:"spends"`
	Peers   int `json:"peers"`
}

// Stats returns a snapshot of tracker state.
func (m *MempoolTracker) Stats() MempoolStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return MempoolStats{
		Entries: len(m.entries),
		UTXOs:   len(m.utxos),
		Spends:  len(m.spends),
		Peers:   len(m.peers),
	}
}
