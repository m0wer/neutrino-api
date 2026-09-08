package neutrino

import (
	"context"
	"errors"
	"testing"

	btcaddress "github.com/btcsuite/btcd/address/v2"
	"github.com/btcsuite/btcd/btcutil/v2"
	"github.com/btcsuite/btcd/btcutil/v2/gcs"
	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/txscript/v2"
	"github.com/btcsuite/btcd/wire/v2"
	"github.com/btcsuite/btclog"
)

func TestGetUTXOValidatesScanSnapshot(t *testing.T) {
	addr, err := btcaddress.DecodeAddress(rescanTestAddress, &chaincfg.RegressionNetParams)
	if err != nil {
		t.Fatal(err)
	}
	script, err := txscript.PayToAddrScript(addr)
	if err != nil {
		t.Fatal(err)
	}
	creation := wire.NewMsgTx(2)
	creation.AddTxOut(wire.NewTxOut(1234, script))
	spend := wire.NewMsgTx(2)
	spend.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: creation.TxHash()}, nil, nil))

	tests := []struct {
		name     string
		mutation string
		spent    bool
		absent   bool
	}{
		{name: "stable unspent"},
		{name: "stable spent", spent: true},
		{name: "stable absent", absent: true},
		{name: "extension preserves scanned snapshot", mutation: "extend"},
		{name: "reorg rejects unspent", mutation: "reorg"},
		{name: "reorg rejects spent", mutation: "reorg", spent: true},
		{name: "reorg rejects absent", mutation: "reorg", absent: true},
		{name: "lower tip rejects snapshot", mutation: "lower"},
		{name: "missing validation header", mutation: "missing"},
		{name: "unavailable validation header", mutation: "unavailable"},
		{name: "reorg during validation", mutation: "validation reorg"},
		{name: "cancel during validation", mutation: "cancel"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			hashes := map[int64]chainhash.Hash{1: {1}, 2: {2}}
			forkHash := chainhash.Hash{3}
			filters := make(map[chainhash.Hash]*gcs.Filter)
			for _, hash := range hashes {
				filters[hash] = newRescanMatchingFilter(t, &hash, addr)
			}
			scanned := false
			reorged := false
			source := &provenanceScanSource{endHeight: 2}
			source.getBlockHash = func(height int64) (*chainhash.Hash, error) {
				if scanned && height == 1 {
					if test.mutation == "validation reorg" {
						reorged = true
					}
					if test.mutation == "cancel" {
						cancel()
					}
				}
				if scanned && height == 2 {
					switch test.mutation {
					case "missing":
						return nil, nil
					case "unavailable":
						return nil, errors.New("header store unavailable")
					}
				}
				if reorged && height == 2 {
					return &forkHash, nil
				}
				hash, ok := hashes[height]
				if !ok {
					t.Fatalf("unexpected header height %d", height)
				}
				return &hash, nil
			}
			source.getCFilter = func(hash chainhash.Hash) (*gcs.Filter, error) {
				return filters[hash], nil
			}
			source.getBlock = func(hash chainhash.Hash) (*btcutil.Block, error) {
				if hash == hashes[1] {
					return newRescanTestBlock(creation), nil
				}
				scanned = true
				switch test.mutation {
				case "reorg":
					reorged = true
				case "lower":
					source.endHeight = 1
				case "extend":
					source.endHeight = 3
				}
				if test.spent {
					return newRescanTestBlock(spend), nil
				}
				return newRescanTestBlock(), nil
			}
			txid := creation.TxHash().String()
			if test.absent {
				txid = provenanceTestTxID
			}
			node := &Node{chainParams: &chaincfg.RegressionNetParams, logger: btclog.Disabled}
			report, err := node.getUTXOWithSource(ctx, txid, 0, rescanTestAddress, 1, source)
			if test.mutation == "cancel" {
				if report != nil || !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled validation returned report=%+v, error=%v", report, err)
				}
				return
			}
			if test.mutation != "" && test.mutation != "extend" {
				var unavailable *UnavailableError
				if report != nil || !errors.As(err, &unavailable) {
					t.Fatalf("invalid snapshot returned report=%+v, error=%v", report, err)
				}
				return
			}
			if test.absent {
				var notFound *NotFoundError
				if report != nil || !errors.As(err, &notFound) {
					t.Fatalf("absent output returned report=%+v, error=%v", report, err)
				}
				return
			}
			if err != nil || report == nil || report.Unspent == test.spent {
				t.Fatalf("stable snapshot returned report=%+v, error=%v", report, err)
			}
			if !test.spent && (report.Value != 1234 || report.BlockHeight != 1) {
				t.Fatalf("wrong creation report: %+v", report)
			}
			if test.spent && (report.SpendingTxID != spend.TxHash().String() || report.SpendingHeight != 2) {
				t.Fatalf("wrong spend report: %+v", report)
			}
		})
	}
}

func TestGetUTXORejectsAddressOfDifferentOutput(t *testing.T) {
	addr, err := btcaddress.DecodeAddress(rescanTestAddress, &chaincfg.RegressionNetParams)
	if err != nil {
		t.Fatal(err)
	}
	wrongScript, err := txscript.PayToAddrScript(addr)
	if err != nil {
		t.Fatal(err)
	}
	actualScript := append([]byte(nil), wrongScript...)
	actualScript[len(actualScript)-1] ^= 1
	creation := wire.NewMsgTx(2)
	creation.AddTxOut(wire.NewTxOut(1234, actualScript))
	creation.AddTxOut(wire.NewTxOut(5678, wrongScript))
	spend := wire.NewMsgTx(2)
	spend.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: creation.TxHash()}, nil, nil))
	hashes := map[int64]chainhash.Hash{1: {1}, 2: {2}}
	creationHash, spendingHash := hashes[1], hashes[2]
	filters := map[chainhash.Hash]*gcs.Filter{
		creationHash: newRescanTestFilter(t, &creationHash, [][]byte{actualScript, wrongScript}),
		spendingHash: newRescanTestFilter(t, &spendingHash, [][]byte{actualScript}),
	}
	source := &provenanceScanSource{
		endHeight: 2,
		getBlockHash: func(height int64) (*chainhash.Hash, error) {
			hash := hashes[height]
			return &hash, nil
		},
		getCFilter: func(hash chainhash.Hash) (*gcs.Filter, error) {
			return filters[hash], nil
		},
		getBlock: func(hash chainhash.Hash) (*btcutil.Block, error) {
			if hash == creationHash {
				return newRescanTestBlock(creation), nil
			}
			return newRescanTestBlock(spend), nil
		},
	}
	node := &Node{chainParams: &chaincfg.RegressionNetParams, logger: btclog.Disabled}
	report, err := node.getUTXOWithSource(
		context.Background(), creation.TxHash().String(), 0, rescanTestAddress, 1, source,
	)
	var badRequest *BadRequestError
	if report != nil || !errors.As(err, &badRequest) {
		t.Fatalf("wrong script yielded report=%+v, error=%v; want invalid-address error", report, err)
	}
}
