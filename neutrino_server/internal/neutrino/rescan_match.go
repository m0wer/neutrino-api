/*
Package neutrino: helpers used by the mempool tracker to match watched
addresses and outpoints against an incoming transaction without exposing
RescanManager's internal maps.
*/
package neutrino

import (
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/txscript/v2"
)

// WatchedScriptIndex returns a snapshot map of pkScript hex -> address string
// for every currently watched address. Returned map is safe for the caller to
// mutate; it is a fresh copy.
func (r *RescanManager) WatchedScriptIndex() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make(map[string]string, len(r.watchedAddrs))
	for addrStr, addr := range r.watchedAddrs {
		script, err := txscript.PayToAddrScript(addr)
		if err != nil {
			r.logger.Debugf("WatchedScriptIndex: skip %s: %v", addrStr, err)
			continue
		}
		out[hex.EncodeToString(script)] = addrStr
	}
	return out
}

// HasWatchedAddresses reports whether any address is currently watched.
func (r *RescanManager) HasWatchedAddresses() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.watchedAddrs) > 0
}

// LookupConfirmedUTXO returns the confirmed UTXO at the given outpoint, if
// one is currently in the rescanned UTXO set. The bool result is false when
// the outpoint is not a watched UTXO.
func (r *RescanManager) LookupConfirmedUTXO(txid string, vout uint32) (UTXO, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	utxo, ok := r.utxoSet[fmt.Sprintf("%s:%d", txid, vout)]
	return utxo, ok
}

// SetMempoolTracker registers a MempoolTracker so the auto-sync pass can
// evict mempool entries that have just been confirmed. Passing nil disables
// the eviction hook.
func (r *RescanManager) SetMempoolTracker(t *MempoolTracker) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mempool = t
}

// notifyMempoolConfirmed is invoked after each successful auto-sync pass
// that downloaded at least one matched block. The caller passes every txid
// observed in those blocks (over-eager: it includes unwatched txs too) and
// the tracker silently ignores any txid it isn't tracking. This is safer
// than wiping the whole mempool view because mempool peers do not reliably
// re-announce already-acked transactions.
func (r *RescanManager) notifyMempoolConfirmed(confirmedTxids []string) {
	if len(confirmedTxids) == 0 {
		return
	}
	r.mu.RLock()
	t := r.mempool
	r.mu.RUnlock()
	if t == nil {
		return
	}
	t.EvictConfirmed(confirmedTxids)
}
