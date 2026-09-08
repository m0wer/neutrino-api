package neutrino

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	btcaddress "github.com/btcsuite/btcd/address/v2"
	"github.com/btcsuite/btcd/btcutil/v2"
	"github.com/btcsuite/btcd/btcutil/v2/gcs"
	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btclog"
)

const (
	provenanceTestAddress = rescanTestAddress
	provenanceTestTxID    = "0000000000000000000000000000000000000000000000000000000000000000"
)

type provenanceScanSource struct {
	endHeight    int32
	getBlockHash func(int64) (*chainhash.Hash, error)
	getCFilter   func(chainhash.Hash) (*gcs.Filter, error)
	getBlock     func(chainhash.Hash) (*btcutil.Block, error)
}

func (s *provenanceScanSource) BestBlock() (int32, error) {
	return s.endHeight, nil
}

func (s *provenanceScanSource) GetBlockHash(height int64) (*chainhash.Hash, error) {
	return s.getBlockHash(height)
}

func (s *provenanceScanSource) GetCFilter(blockHash chainhash.Hash) (*gcs.Filter, error) {
	return s.getCFilter(blockHash)
}

func (s *provenanceScanSource) GetBlock(blockHash chainhash.Hash) (*btcutil.Block, error) {
	return s.getBlock(blockHash)
}

func provenanceHash(byteValue byte) chainhash.Hash {
	return chainhash.Hash{byteValue}
}

func newProvenanceTestNode(utxo UTXO) *Node {
	return &Node{
		chainParams: &chaincfg.RegressionNetParams,
		logger:      btclog.Disabled,
		rescanMgr: &RescanManager{
			logger:  btclog.Disabled,
			utxoSet: map[string]UTXO{fmt.Sprintf("%s:%d", utxo.TxID, utxo.Vout): utxo},
		},
	}
}

func TestGetUTXOCacheRequiresCurrentVerifiedAnchor(t *testing.T) {
	currentHash := provenanceHash(1)
	differentHash := provenanceHash(2)

	baseUTXO := UTXO{
		TxID:              provenanceTestTxID,
		Vout:              0,
		Value:             100_000,
		Address:           provenanceTestAddress,
		ScriptPubKey:      "76a914000000000000000000000000000000000000000088ac",
		Height:            90,
		VerifiedTipHeight: 100,
		VerifiedTipHash:   currentHash.String(),
	}

	tests := []struct {
		name      string
		utxo      UTXO
		chainTip  int32
		getHash   func(int64) (*chainhash.Hash, error)
		wantCalls []int64
	}{
		{
			name:      "same-height different hash falls through",
			utxo:      baseUTXO,
			chainTip:  100,
			wantCalls: []int64{100, 100, 90},
			getHash: func(height int64) (*chainhash.Hash, error) {
				if height == 100 {
					return &differentHash, nil
				}
				return nil, errors.New("historical scan started")
			},
		},
		{
			name:      "lower tip falls through",
			utxo:      baseUTXO,
			chainTip:  99,
			wantCalls: []int64{99},
			getHash: func(int64) (*chainhash.Hash, error) {
				return nil, errors.New("historical scan started")
			},
		},
		{
			name: "old anchor gap falls through",
			utxo: func() UTXO {
				utxo := baseUTXO
				utxo.VerifiedTipHeight = 99
				return utxo
			}(),
			chainTip:  100,
			wantCalls: []int64{100},
			getHash: func(int64) (*chainhash.Hash, error) {
				return nil, errors.New("historical scan started")
			},
		},
		{
			name: "malformed anchor falls through",
			utxo: func() UTXO {
				utxo := baseUTXO
				utxo.VerifiedTipHash = "not-a-block-hash"
				return utxo
			}(),
			chainTip:  100,
			wantCalls: []int64{100},
			getHash: func(int64) (*chainhash.Hash, error) {
				return nil, errors.New("historical scan started")
			},
		},
		{
			name:      "unavailable anchor falls through",
			utxo:      baseUTXO,
			chainTip:  100,
			wantCalls: []int64{100, 100},
			getHash: func(int64) (*chainhash.Hash, error) {
				return nil, errors.New("header unavailable")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := make([]int64, 0)
			source := &provenanceScanSource{
				endHeight: test.chainTip,
				getBlockHash: func(height int64) (*chainhash.Hash, error) {
					calls = append(calls, height)
					return test.getHash(height)
				},
				getCFilter: func(chainhash.Hash) (*gcs.Filter, error) {
					return nil, errors.New("unexpected compact filter request")
				},
				getBlock: func(chainhash.Hash) (*btcutil.Block, error) {
					return nil, errors.New("unexpected block request")
				},
			}

			report, err := newProvenanceTestNode(test.utxo).getUTXOWithSource(
				context.Background(), provenanceTestTxID, 0, provenanceTestAddress, 90, source,
			)
			if report != nil {
				t.Fatalf("cache was trusted despite invalid provenance: %+v", report)
			}
			var unavailableErr *UnavailableError
			if !errors.As(err, &unavailableErr) || unavailableErr.Resource != "block hash" {
				t.Fatalf("cache did not fall through to the historical scan: %v", err)
			}
			if fmt.Sprint(calls) != fmt.Sprint(test.wantCalls) {
				t.Fatalf("block hash calls = %v, want %v", calls, test.wantCalls)
			}
		})
	}
}

func TestGetUTXOHashlessPersistedUTXOFallsThroughWithoutMutation(t *testing.T) {
	store, err := OpenStateStore(t.TempDir(), btclog.Disabled)
	if err != nil {
		t.Fatalf("OpenStateStore() error: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("StateStore.Close() error: %v", err)
		}
	})

	key := provenanceTestTxID + ":0"
	persistedUTXO := UTXO{
		TxID: provenanceTestTxID, Vout: 0, Value: 100_000,
		Address: provenanceTestAddress, Height: 90,
	}
	if err := store.SaveUTXOSet(map[string]UTXO{key: persistedUTXO}); err != nil {
		t.Fatalf("SaveUTXOSet() error: %v", err)
	}

	mgr := &RescanManager{
		chainParams:  &chaincfg.RegressionNetParams,
		logger:       btclog.Disabled,
		store:        store,
		watchedAddrs: make(map[string]btcaddress.Address),
		utxoSet:      make(map[string]UTXO),
	}
	mgr.loadPersistedState()

	headerCalls := 0
	source := &provenanceScanSource{
		endHeight: 100,
		getBlockHash: func(int64) (*chainhash.Hash, error) {
			headerCalls++
			return nil, errors.New("historical scan started")
		},
		getCFilter: func(chainhash.Hash) (*gcs.Filter, error) {
			return nil, errors.New("unexpected compact filter request")
		},
		getBlock: func(chainhash.Hash) (*btcutil.Block, error) {
			return nil, errors.New("unexpected block request")
		},
	}
	node := &Node{chainParams: &chaincfg.RegressionNetParams, logger: btclog.Disabled, rescanMgr: mgr}
	_, err = node.getUTXOWithSource(
		context.Background(), provenanceTestTxID, 0, provenanceTestAddress, 90, source,
	)
	var unavailableErr *UnavailableError
	if !errors.As(err, &unavailableErr) || unavailableErr.Resource != "block hash" {
		t.Fatalf("hashless persisted UTXO did not fall through to historical scan: %v", err)
	}
	if headerCalls != 1 {
		t.Fatalf("header calls = %d, want only the on-demand historical scan", headerCalls)
	}
	if got, exists := mgr.utxoSet[key]; !exists || got != persistedUTXO {
		t.Fatalf("hashless persisted UTXO was changed in memory: %+v, exists=%v", got, exists)
	}
	loaded, err := store.LoadUTXOSet()
	if err != nil {
		t.Fatalf("LoadUTXOSet() error: %v", err)
	}
	if got, exists := loaded[key]; !exists || got != persistedUTXO {
		t.Fatalf("hashless persisted UTXO was changed on disk: %+v, exists=%v", got, exists)
	}
}

func TestUTXOProvenanceJSONIsOptional(t *testing.T) {
	withoutAnchor := UTXO{TxID: provenanceTestTxID}
	data, err := json.Marshal(withoutAnchor)
	if err != nil {
		t.Fatalf("json.Marshal() error: %v", err)
	}
	if strings.Contains(string(data), "verified_tip_") {
		t.Fatalf("unanchored UTXO serialized provenance: %s", data)
	}
}
