package neutrino

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	btcaddress "github.com/btcsuite/btcd/address/v2"
	"github.com/btcsuite/btcd/btcutil/v2"
	"github.com/btcsuite/btcd/btcutil/v2/gcs"
	"github.com/btcsuite/btcd/btcutil/v2/gcs/builder"
	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/txscript/v2"
	"github.com/btcsuite/btclog"
	"golang.org/x/net/proxy"
)

type mockUTXOScanSource struct {
	endHeight    int32
	bestBlockErr error
	getBlockHash func(int64) (*chainhash.Hash, error)
	getCFilter   func(chainhash.Hash) (*gcs.Filter, error)
	getBlock     func(chainhash.Hash) (*btcutil.Block, error)
}

func (s *mockUTXOScanSource) BestBlock() (int32, error) {
	return s.endHeight, s.bestBlockErr
}

func (s *mockUTXOScanSource) GetBlockHash(height int64) (*chainhash.Hash, error) {
	return s.getBlockHash(height)
}

func (s *mockUTXOScanSource) GetCFilter(blockHash chainhash.Hash) (*gcs.Filter, error) {
	return s.getCFilter(blockHash)
}

func (s *mockUTXOScanSource) GetBlock(blockHash chainhash.Hash) (*btcutil.Block, error) {
	return s.getBlock(blockHash)
}

func TestNewNode(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)

	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name:    "nil config",
			config:  nil,
			wantErr: true,
		},
		{
			name: "invalid network",
			config: &Config{
				Network: "invalid",
				DataDir: "/tmp/test",
				Logger:  backend,
			},
			wantErr: true,
		},
		{
			name: "valid mainnet config",
			config: &Config{
				Network:         "mainnet",
				DataDir:         "/tmp/test",
				MaxPeers:        8,
				BanDuration:     24 * time.Hour,
				FilterCacheSize: 4096,
				Logger:          backend,
				LogLevel:        "info",
			},
			wantErr: false,
		},
		{
			name: "valid testnet config",
			config: &Config{
				Network:         "testnet",
				DataDir:         "/tmp/test",
				MaxPeers:        8,
				BanDuration:     24 * time.Hour,
				FilterCacheSize: 4096,
				Logger:          backend,
				LogLevel:        "debug",
			},
			wantErr: false,
		},
		{
			name: "valid regtest config",
			config: &Config{
				Network:         "regtest",
				DataDir:         "/tmp/test",
				AddPeers:        "localhost:18444",
				MaxPeers:        8,
				BanDuration:     24 * time.Hour,
				FilterCacheSize: 4096,
				Logger:          backend,
				LogLevel:        "trace",
			},
			wantErr: false,
		},
		{
			name: "valid signet config",
			config: &Config{
				Network:         "signet",
				DataDir:         "/tmp/test",
				MaxPeers:        8,
				BanDuration:     24 * time.Hour,
				FilterCacheSize: 4096,
				Logger:          backend,
				LogLevel:        "warn",
			},
			wantErr: false,
		},
		{
			name: "valid config with Tor proxy",
			config: &Config{
				Network:         "mainnet",
				DataDir:         "/tmp/test",
				TorProxy:        "127.0.0.1:9050",
				MaxPeers:        8,
				BanDuration:     24 * time.Hour,
				FilterCacheSize: 4096,
				Logger:          backend,
				LogLevel:        "info",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node, err := NewNode(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewNode() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && node == nil {
				t.Error("NewNode() returned nil node without error")
			}
		})
	}
}

func TestGetChainParams(t *testing.T) {
	tests := []struct {
		network string
		wantErr bool
	}{
		{"mainnet", false},
		{"testnet", false},
		{"regtest", false},
		{"signet", false},
		{"invalid", true},
		{"", true},
	}

	for _, tt := range tests {
		t.Run(tt.network, func(t *testing.T) {
			params, err := getChainParams(tt.network)
			if (err != nil) != tt.wantErr {
				t.Errorf("getChainParams(%s) error = %v, wantErr %v", tt.network, err, tt.wantErr)
				return
			}
			if !tt.wantErr && params == nil {
				t.Errorf("getChainParams(%s) returned nil params without error", tt.network)
			}
		})
	}
}

func TestGetDNSSeeds(t *testing.T) {
	tests := []struct {
		network   string
		wantSeeds bool
	}{
		{"mainnet", true},
		{"testnet", true},
		{"signet", true},
		{"regtest", false},
		{"invalid", false},
	}

	for _, tt := range tests {
		t.Run(tt.network, func(t *testing.T) {
			seeds := getDNSSeeds(tt.network)
			hasSeeds := len(seeds) > 0
			if hasSeeds != tt.wantSeeds {
				t.Errorf("getDNSSeeds(%s) returned %d seeds, wantSeeds = %v", tt.network, len(seeds), tt.wantSeeds)
			}
		})
	}
}

func TestGetStatus(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)

	config := &Config{
		Network:         "regtest",
		DataDir:         "/tmp/test",
		MaxPeers:        8,
		BanDuration:     24 * time.Hour,
		FilterCacheSize: 4096,
		Logger:          backend,
		LogLevel:        "info",
	}

	node, err := NewNode(config)
	if err != nil {
		t.Fatalf("NewNode() failed: %v", err)
	}

	status := node.GetStatus()

	// Initial status should be not synced
	if status.Synced {
		t.Error("expected node to not be synced initially")
	}

	if status.BlockHeight != 0 {
		t.Errorf("expected block height 0, got %d", status.BlockHeight)
	}

	if status.Peers != 0 {
		t.Errorf("expected 0 peers, got %d", status.Peers)
	}

	if status.Version != "dev" {
		t.Errorf("expected default version dev, got %q", status.Version)
	}

	if status.WatchedAddresses != 0 {
		t.Errorf("expected watched address count 0, got %d", status.WatchedAddresses)
	}
}

func TestGetStatusIncludesConfiguredVersion(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)

	node, err := NewNode(&Config{
		Network:         "regtest",
		DataDir:         "/tmp/test",
		Version:         "v1.2.3-test",
		FilterCacheSize: 4096,
		Logger:          backend,
		LogLevel:        "info",
	})
	if err != nil {
		t.Fatalf("NewNode() failed: %v", err)
	}

	status := node.GetStatus()
	if status.Version != "v1.2.3-test" {
		t.Errorf("expected configured version, got %q", status.Version)
	}
}

func TestComputePrefetchWorkerCount(t *testing.T) {
	tests := []struct {
		name       string
		configured int
		expected   int
	}{
		{name: "configured", configured: 6, expected: 6},
		{name: "configured capped", configured: 64, expected: 32},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computePrefetchWorkerCount(tt.configured)
			if got != tt.expected {
				t.Fatalf("expected %d workers, got %d", tt.expected, got)
			}
		})
	}

	auto := computePrefetchWorkerCount(0)
	if auto < 4 || auto > 16 {
		t.Fatalf("auto worker count out of bounds: %d", auto)
	}
}

func TestUTXOSpendReportJSON(t *testing.T) {
	tests := []struct {
		name     string
		report   UTXOSpendReport
		wantKeys []string // JSON keys that must be present
		noKeys   []string // JSON keys that must NOT be present (omitempty)
	}{
		{
			name: "unspent with block_height",
			report: UTXOSpendReport{
				Unspent:      true,
				Value:        100000,
				ScriptPubKey: "00140000000000000000000000000000000000000000",
				BlockHeight:  295000,
			},
			wantKeys: []string{"unspent", "value", "scriptpubkey", "block_height"},
			noKeys:   []string{"spending_txid", "spending_input", "spending_height"},
		},
		{
			name: "unspent without block_height (omitempty)",
			report: UTXOSpendReport{
				Unspent:      true,
				Value:        50000,
				ScriptPubKey: "00140000000000000000000000000000000000000000",
				BlockHeight:  0,
			},
			wantKeys: []string{"unspent", "value", "scriptpubkey"},
			noKeys:   []string{"block_height"},
		},
		{
			name: "spent UTXO",
			report: UTXOSpendReport{
				Unspent:        false,
				SpendingTxID:   "abcd1234",
				SpendingInput:  0,
				SpendingHeight: 295001,
			},
			wantKeys: []string{"unspent", "spending_txid", "spending_height"},
			noKeys:   []string{"value", "scriptpubkey", "block_height"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.report)
			if err != nil {
				t.Fatalf("json.Marshal() error: %v", err)
			}

			var parsed map[string]interface{}
			if err := json.Unmarshal(data, &parsed); err != nil {
				t.Fatalf("json.Unmarshal() error: %v", err)
			}

			for _, key := range tt.wantKeys {
				if _, ok := parsed[key]; !ok {
					t.Errorf("expected key %q in JSON, got: %s", key, string(data))
				}
			}

			for _, key := range tt.noKeys {
				if _, ok := parsed[key]; ok {
					t.Errorf("unexpected key %q in JSON (omitempty), got: %s", key, string(data))
				}
			}

			// Verify block_height value when present
			if tt.report.BlockHeight > 0 {
				if bh, ok := parsed["block_height"].(float64); !ok {
					t.Errorf("block_height is not a number: %v", parsed["block_height"])
				} else if uint32(bh) != tt.report.BlockHeight {
					t.Errorf("block_height = %v, want %d", bh, tt.report.BlockHeight)
				}
			}
		})
	}
}

func TestGetUTXOAbortsOnUnavailableScanData(t *testing.T) {
	const (
		address = "1BoatSLRHtKNngkdXEeobR76b53LETtpyT"
		txid    = "0000000000000000000000000000000000000000000000000000000000000000"
	)

	blockHash := chainhash.Hash{1}
	matchingFilter := newMatchingFilter(t, address, &blockHash)

	tests := []struct {
		name     string
		resource string
		source   *mockUTXOScanSource
	}{
		{
			name:     "block hash",
			resource: "block hash",
			source: &mockUTXOScanSource{
				endHeight: 1,
				getBlockHash: func(int64) (*chainhash.Hash, error) {
					return nil, errors.New("header store unavailable")
				},
				getCFilter: func(chainhash.Hash) (*gcs.Filter, error) {
					return nil, errors.New("unexpected compact filter request")
				},
				getBlock: func(chainhash.Hash) (*btcutil.Block, error) {
					return nil, errors.New("unexpected block request")
				},
			},
		},
		{
			name:     "compact filter",
			resource: "compact filter",
			source: &mockUTXOScanSource{
				endHeight: 1,
				getBlockHash: func(int64) (*chainhash.Hash, error) {
					return &blockHash, nil
				},
				getCFilter: func(chainhash.Hash) (*gcs.Filter, error) {
					return nil, errors.New("peer did not provide filter")
				},
				getBlock: func(chainhash.Hash) (*btcutil.Block, error) {
					return nil, errors.New("unexpected block request")
				},
			},
		},
		{
			name:     "block",
			resource: "block",
			source: &mockUTXOScanSource{
				endHeight: 1,
				getBlockHash: func(int64) (*chainhash.Hash, error) {
					return &blockHash, nil
				},
				getCFilter: func(chainhash.Hash) (*gcs.Filter, error) {
					return matchingFilter, nil
				},
				getBlock: func(chainhash.Hash) (*btcutil.Block, error) {
					return nil, errors.New("peer did not provide block")
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := &Node{chainParams: &chaincfg.MainNetParams, logger: btclog.Disabled}
			report, err := node.getUTXOWithSource(context.Background(), txid, 0, address, 1, tt.source)
			if report != nil {
				t.Fatalf("unexpected UTXO report after unavailable %s", tt.resource)
			}

			var unavailableErr *UnavailableError
			if !errors.As(err, &unavailableErr) {
				t.Fatalf("expected UnavailableError, got %v", err)
			}
			if unavailableErr.Resource != tt.resource {
				t.Errorf("unavailable resource = %q, want %q", unavailableErr.Resource, tt.resource)
			}
		})
	}
}

func TestGetUTXOCancellationStopsScan(t *testing.T) {
	const address = "1BoatSLRHtKNngkdXEeobR76b53LETtpyT"

	blockHash := chainhash.Hash{1}
	started := make(chan struct{})
	release := make(chan struct{})
	source := &mockUTXOScanSource{
		endHeight: 1,
		getBlockHash: func(int64) (*chainhash.Hash, error) {
			return &blockHash, nil
		},
		getCFilter: func(chainhash.Hash) (*gcs.Filter, error) {
			close(started)
			<-release
			return nil, errors.New("peer query ended")
		},
		getBlock: func(chainhash.Hash) (*btcutil.Block, error) {
			return nil, errors.New("unexpected block request")
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := (&Node{chainParams: &chaincfg.MainNetParams, logger: btclog.Disabled}).getUTXOWithSource(
			ctx,
			"0000000000000000000000000000000000000000000000000000000000000000",
			0,
			address,
			1,
			source,
		)
		result <- err
	}()

	<-started
	cancel()

	select {
	case err := <-result:
		t.Fatalf("scan returned before the in-flight query completed: %v", err)
	default:
	}

	close(release)
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("UTXO scan did not stop after the in-flight query completed")
	}
}

func TestGetUTXOUsesOnlyCurrentRescannedState(t *testing.T) {
	const (
		address = "1BoatSLRHtKNngkdXEeobR76b53LETtpyT"
		txid    = "0000000000000000000000000000000000000000000000000000000000000000"
	)
	anchorHash := chainhash.Hash{1}

	newNode := func(scannedTip int32) *Node {
		return &Node{
			chainParams: &chaincfg.MainNetParams,
			logger:      btclog.Disabled,
			rescanMgr: &RescanManager{
				utxoSet: map[string]UTXO{
					fmt.Sprintf("%s:%d", txid, 0): {
						TxID:              txid,
						Vout:              0,
						Value:             100_000,
						Address:           address,
						ScriptPubKey:      "76a914000000000000000000000000000000000000000088ac",
						Height:            90,
						VerifiedTipHeight: scannedTip,
						VerifiedTipHash:   anchorHash.String(),
					},
				},
				status: RescanStatus{LastScannedTip: scannedTip},
			},
		}
	}

	t.Run("current state returns without historical scan", func(t *testing.T) {
		source := &mockUTXOScanSource{
			endHeight: 100,
			getBlockHash: func(height int64) (*chainhash.Hash, error) {
				if height != 100 {
					t.Fatalf("current cached UTXO unexpectedly started historical scan at height %d", height)
				}
				return &anchorHash, nil
			},
		}
		report, err := newNode(100).getUTXOWithSource(
			context.Background(), txid, 0, address, 90, source,
		)
		if err != nil {
			t.Fatalf("getUTXOWithSource() error = %v", err)
		}
		if !report.Unspent || report.Value != 100_000 || report.BlockHeight != 90 {
			t.Fatalf("unexpected cached report: %+v", report)
		}
	})

	t.Run("stale state falls through to historical scan", func(t *testing.T) {
		source := &mockUTXOScanSource{
			endHeight: 100,
			getBlockHash: func(int64) (*chainhash.Hash, error) {
				return nil, errors.New("historical scan started")
			},
			getCFilter: func(chainhash.Hash) (*gcs.Filter, error) {
				return nil, errors.New("unexpected compact filter request")
			},
			getBlock: func(chainhash.Hash) (*btcutil.Block, error) {
				return nil, errors.New("unexpected block request")
			},
		}
		_, err := newNode(99).getUTXOWithSource(
			context.Background(), txid, 0, address, 90, source,
		)
		var unavailableErr *UnavailableError
		if !errors.As(err, &unavailableErr) || unavailableErr.Resource != "block hash" {
			t.Fatalf("stale cache did not fall through to historical scan: %v", err)
		}
	})

	t.Run("creation before start height falls through to historical scan", func(t *testing.T) {
		source := &mockUTXOScanSource{
			endHeight: 100,
			getBlockHash: func(int64) (*chainhash.Hash, error) {
				return nil, errors.New("historical scan started")
			},
			getCFilter: func(chainhash.Hash) (*gcs.Filter, error) {
				return nil, errors.New("unexpected compact filter request")
			},
			getBlock: func(chainhash.Hash) (*btcutil.Block, error) {
				return nil, errors.New("unexpected block request")
			},
		}
		_, err := newNode(100).getUTXOWithSource(
			context.Background(), txid, 0, address, 91, source,
		)
		var unavailableErr *UnavailableError
		if !errors.As(err, &unavailableErr) || unavailableErr.Resource != "block hash" {
			t.Fatalf("out-of-range cache did not fall through to historical scan: %v", err)
		}
	})
}

func newMatchingFilter(t *testing.T, address string, blockHash *chainhash.Hash) *gcs.Filter {
	t.Helper()

	addr, err := btcaddress.DecodeAddress(address, &chaincfg.MainNetParams)
	if err != nil {
		t.Fatalf("DecodeAddress() error = %v", err)
	}
	pkScript, err := txscript.PayToAddrScript(addr)
	if err != nil {
		t.Fatalf("PayToAddrScript() error = %v", err)
	}

	filter, err := gcs.BuildGCSFilter(
		builder.DefaultP,
		builder.DefaultM,
		builder.DeriveKey(blockHash),
		[][]byte{pkScript},
	)
	if err != nil {
		t.Fatalf("BuildGCSFilter() error = %v", err)
	}
	return filter
}

func TestNewNodeWithCFilterCDNConfig(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)

	tests := []struct {
		name    string
		cdnAuto bool
		cdnURL  string
	}{
		{
			name:    "auto enabled",
			cdnAuto: true,
			cdnURL:  "",
		},
		{
			name:    "explicit URL",
			cdnAuto: false,
			cdnURL:  "https://example.com",
		},
		{
			name:    "both disabled",
			cdnAuto: false,
			cdnURL:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node, err := NewNode(&Config{
				Network:         "regtest",
				DataDir:         "/tmp/test",
				FilterCacheSize: 4096,
				Logger:          backend,
				LogLevel:        "info",
				CFilterCDNAuto:  tt.cdnAuto,
				CFilterCDNURL:   tt.cdnURL,
			})
			if err != nil {
				t.Fatalf("NewNode() failed: %v", err)
			}
			if node == nil {
				t.Fatal("NewNode() returned nil")
			}
			if node.config.CFilterCDNAuto != tt.cdnAuto {
				t.Errorf("CFilterCDNAuto = %v, want %v",
					node.config.CFilterCDNAuto, tt.cdnAuto)
			}
			if node.config.CFilterCDNURL != tt.cdnURL {
				t.Errorf("CFilterCDNURL = %q, want %q",
					node.config.CFilterCDNURL, tt.cdnURL)
			}
		})
	}
}

// mockDialer implements proxy.Dialer for testing.
type mockDialer struct{}

func (d *mockDialer) Dial(network, addr string) (net.Conn, error) {
	return nil, net.ErrClosed
}

// Compile-time check that mockDialer implements proxy.Dialer.
var _ proxy.Dialer = (*mockDialer)(nil)

func TestNewTorHTTPClient(t *testing.T) {
	client := newTorHTTPClient(&mockDialer{})
	if client == nil {
		t.Fatal("newTorHTTPClient() returned nil")
	}

	// Verify it's an *http.Client.
	httpClient, ok := any(client).(*http.Client)
	if !ok {
		t.Fatal("newTorHTTPClient() did not return *http.Client")
	}

	// Verify a custom transport is set.
	if httpClient.Transport == nil {
		t.Fatal("expected custom transport, got nil")
	}

	transport, ok := httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", httpClient.Transport)
	}

	// Verify DialContext is set (the SOCKS5 wrapper).
	if transport.DialContext == nil {
		t.Fatal("expected DialContext to be set on transport")
	}
}

func TestDefaultBlockDNProvider(t *testing.T) {
	tests := []struct {
		network       string
		wantBaseURL   string
		wantChainName string
		wantOK        bool
	}{
		{"mainnet", "https://block-dn.org", "mainnet", true},
		{"signet", "https://signet.block-dn.org", "signet", true},
		{"testnet", "https://testnet3.block-dn.org", "testnet3", true},
		{"regtest", "", "", false},
		{"invalid", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.network, func(t *testing.T) {
			baseURL, chainName, ok := defaultBlockDNProvider(tt.network)
			if ok != tt.wantOK {
				t.Fatalf("ok=%v want %v", ok, tt.wantOK)
			}
			if baseURL != tt.wantBaseURL {
				t.Fatalf("baseURL=%q want %q", baseURL, tt.wantBaseURL)
			}
			if chainName != tt.wantChainName {
				t.Fatalf("chainName=%q want %q", chainName, tt.wantChainName)
			}
		})
	}
}

func TestResolveCFilterCDNBaseURL(t *testing.T) {
	backend := btclog.NewBackend(os.Stdout)

	newTestNode := func(cfg *Config) *Node {
		n, err := NewNode(cfg)
		if err != nil {
			t.Fatalf("NewNode() failed: %v", err)
		}
		return n
	}

	t.Run("explicit URL", func(t *testing.T) {
		n := newTestNode(&Config{
			Network:         "regtest",
			DataDir:         "/tmp/test",
			FilterCacheSize: 4096,
			Logger:          backend,
			LogLevel:        "info",
			CFilterCDNURL:   "https://example.com/cdn/",
		})

		got := n.resolveCFilterCDNBaseURL()
		if got != "https://example.com/cdn" {
			t.Fatalf("expected trimmed URL, got %q", got)
		}
	})

	t.Run("auto mainnet", func(t *testing.T) {
		n := newTestNode(&Config{
			Network:         "mainnet",
			DataDir:         "/tmp/test",
			FilterCacheSize: 4096,
			Logger:          backend,
			LogLevel:        "info",
			CFilterCDNAuto:  true,
		})

		got := n.resolveCFilterCDNBaseURL()
		if got != "https://block-dn.org" {
			t.Fatalf("expected block-dn mainnet URL, got %q", got)
		}
	})

	t.Run("auto disabled no URL", func(t *testing.T) {
		n := newTestNode(&Config{
			Network:         "mainnet",
			DataDir:         "/tmp/test",
			FilterCacheSize: 4096,
			Logger:          backend,
			LogLevel:        "info",
			CFilterCDNAuto:  false,
		})

		got := n.resolveCFilterCDNBaseURL()
		if got != "" {
			t.Fatalf("expected empty URL, got %q", got)
		}
	})

	t.Run("auto unsupported network", func(t *testing.T) {
		n := newTestNode(&Config{
			Network:         "regtest",
			DataDir:         "/tmp/test",
			FilterCacheSize: 4096,
			Logger:          backend,
			LogLevel:        "info",
			CFilterCDNAuto:  true,
		})

		got := n.resolveCFilterCDNBaseURL()
		if got != "" {
			t.Fatalf("expected empty URL for regtest, got %q", got)
		}
	})

	t.Run("explicit overrides auto", func(t *testing.T) {
		n := newTestNode(&Config{
			Network:         "mainnet",
			DataDir:         "/tmp/test",
			FilterCacheSize: 4096,
			Logger:          backend,
			LogLevel:        "info",
			CFilterCDNAuto:  true,
			CFilterCDNURL:   "https://custom.example.com",
		})

		got := n.resolveCFilterCDNBaseURL()
		if got != "https://custom.example.com" {
			t.Fatalf("expected custom URL, got %q", got)
		}
	})
}

func TestParseCFilterChunk(t *testing.T) {
	t.Run("single small filter", func(t *testing.T) {
		// CompactSize(4) = 0x04, filter = 4 bytes
		data := []byte{0x04, 0x01, 0x7f, 0xa8, 0x80}
		filters, err := parseCFilterChunk(data, 1)
		if err != nil {
			t.Fatalf("parseCFilterChunk() error: %v", err)
		}
		if len(filters) != 1 {
			t.Fatalf("expected 1 filter, got %d", len(filters))
		}
		if len(filters[0]) != 4 {
			t.Fatalf("expected 4 bytes, got %d", len(filters[0]))
		}
	})

	t.Run("multiple filters", func(t *testing.T) {
		// Two filters: CompactSize(2) + 2 bytes, CompactSize(3) + 3 bytes
		data := []byte{
			0x02, 0xaa, 0xbb, // filter 1: len=2, data=aa bb
			0x03, 0xcc, 0xdd, 0xee, // filter 2: len=3, data=cc dd ee
		}
		filters, err := parseCFilterChunk(data, 2)
		if err != nil {
			t.Fatalf("parseCFilterChunk() error: %v", err)
		}
		if len(filters) != 2 {
			t.Fatalf("expected 2 filters, got %d", len(filters))
		}
		if len(filters[0]) != 2 || filters[0][0] != 0xaa {
			t.Fatalf("unexpected filter 0: %x", filters[0])
		}
		if len(filters[1]) != 3 || filters[1][0] != 0xcc {
			t.Fatalf("unexpected filter 1: %x", filters[1])
		}
	})

	t.Run("count mismatch", func(t *testing.T) {
		data := []byte{0x02, 0xaa, 0xbb}
		_, err := parseCFilterChunk(data, 2)
		if err == nil {
			t.Fatal("expected error for count mismatch")
		}
	})

	t.Run("truncated data", func(t *testing.T) {
		// Says length 10 but only has 2 bytes
		data := []byte{0x0a, 0xaa, 0xbb}
		_, err := parseCFilterChunk(data, 1)
		if err == nil {
			t.Fatal("expected error for truncated data")
		}
	})

	t.Run("empty data empty count", func(t *testing.T) {
		filters, err := parseCFilterChunk([]byte{}, 0)
		if err != nil {
			t.Fatalf("parseCFilterChunk() error: %v", err)
		}
		if len(filters) != 0 {
			t.Fatalf("expected 0 filters, got %d", len(filters))
		}
	})

	t.Run("two-byte CompactSize", func(t *testing.T) {
		// CompactSize for value 300 (0x012c): fd 2c 01
		data := make([]byte, 3+300)
		data[0] = 0xfd
		data[1] = 0x2c // low byte
		data[2] = 0x01 // high byte
		for i := range 300 {
			data[3+i] = byte(i)
		}
		filters, err := parseCFilterChunk(data, 1)
		if err != nil {
			t.Fatalf("parseCFilterChunk() error: %v", err)
		}
		if len(filters) != 1 || len(filters[0]) != 300 {
			t.Fatalf("expected 1 filter of 300 bytes, got %d filters, first len %d",
				len(filters), len(filters[0]))
		}
	})
}

func TestComputeCDNChunkBounds(t *testing.T) {
	tests := []struct {
		name          string
		startHeight   int32
		bestFilter    int32
		bestBlock     int32
		chunkSize     int
		wantFirst     int32
		wantLast      int32
		wantHasChunks bool
	}{
		{
			name:          "no full chunk yet",
			startHeight:   0,
			bestFilter:    1500,
			bestBlock:     1500,
			chunkSize:     2000,
			wantFirst:     0,
			wantLast:      0,
			wantHasChunks: false,
		},
		{
			name:          "exactly one full chunk",
			startHeight:   0,
			bestFilter:    1999,
			bestBlock:     1999,
			chunkSize:     2000,
			wantFirst:     0,
			wantLast:      0,
			wantHasChunks: true,
		},
		{
			name:          "multiple full chunks",
			startHeight:   0,
			bestFilter:    4999,
			bestBlock:     4999,
			chunkSize:     2000,
			wantFirst:     0,
			wantLast:      2000,
			wantHasChunks: true,
		},
		{
			name:          "start inside first chunk floor-aligns",
			startHeight:   1,
			bestFilter:    3999,
			bestBlock:     3999,
			chunkSize:     2000,
			wantFirst:     0,
			wantLast:      2000,
			wantHasChunks: true,
		},
		{
			name:          "start mid-chunk floor-aligns",
			startHeight:   500,
			bestFilter:    5999,
			bestBlock:     5999,
			chunkSize:     2000,
			wantFirst:     0,
			wantLast:      4000,
			wantHasChunks: true,
		},
		{
			name:          "start on chunk boundary",
			startHeight:   2000,
			bestFilter:    5999,
			bestBlock:     5999,
			chunkSize:     2000,
			wantFirst:     2000,
			wantLast:      4000,
			wantHasChunks: true,
		},
		{
			name:          "fallback to best block",
			startHeight:   0,
			bestFilter:    0,
			bestBlock:     3999,
			chunkSize:     2000,
			wantFirst:     0,
			wantLast:      2000,
			wantHasChunks: true,
		},
		{
			name:          "start beyond available chunks",
			startHeight:   4000,
			bestFilter:    3999,
			bestBlock:     3999,
			chunkSize:     2000,
			wantFirst:     4000,
			wantLast:      2000,
			wantHasChunks: false,
		},
		{
			name:          "zero chunk size",
			startHeight:   0,
			bestFilter:    5999,
			bestBlock:     5999,
			chunkSize:     0,
			wantFirst:     0,
			wantLast:      0,
			wantHasChunks: false,
		},
		{
			name:          "negative chunk size",
			startHeight:   0,
			bestFilter:    5999,
			bestBlock:     5999,
			chunkSize:     -1,
			wantFirst:     0,
			wantLast:      0,
			wantHasChunks: false,
		},
		{
			name:          "realistic mainnet lookback",
			startHeight:   844338,
			bestFilter:    944338,
			bestBlock:     944338,
			chunkSize:     2000,
			wantFirst:     844000,
			wantLast:      942000,
			wantHasChunks: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first, last, hasChunks := computeCDNChunkBounds(
				tt.startHeight,
				tt.bestFilter,
				tt.bestBlock,
				tt.chunkSize,
			)

			if first != tt.wantFirst {
				t.Fatalf("first=%d want %d", first, tt.wantFirst)
			}
			if last != tt.wantLast {
				t.Fatalf("last=%d want %d", last, tt.wantLast)
			}
			if hasChunks != tt.wantHasChunks {
				t.Fatalf("hasChunks=%v want %v", hasChunks, tt.wantHasChunks)
			}
		})
	}
}

func TestCDNLastHeightAdvanceRules(t *testing.T) {
	tests := []struct {
		name               string
		start              int32
		cdnImported        int
		cdnLastHeight      int32
		coveredFromStart   bool
		wantStart          int32
		wantPrefetchUpdate bool
	}{
		{
			name:               "covered from requested start advances",
			start:              100,
			cdnImported:        2000,
			cdnLastHeight:      2099,
			coveredFromStart:   true,
			wantStart:          2100,
			wantPrefetchUpdate: true,
		},
		{
			name:               "not covered does not advance",
			start:              100,
			cdnImported:        2000,
			cdnLastHeight:      3999,
			coveredFromStart:   false,
			wantStart:          100,
			wantPrefetchUpdate: false,
		},
		{
			name:               "no import does not advance",
			start:              100,
			cdnImported:        0,
			cdnLastHeight:      99,
			coveredFromStart:   false,
			wantStart:          100,
			wantPrefetchUpdate: false,
		},
		{
			name:               "floor-aligned start covers from before",
			start:              500,
			cdnImported:        4000,
			cdnLastHeight:      3999,
			coveredFromStart:   true,
			wantStart:          4000,
			wantPrefetchUpdate: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := tt.start
			prefetchLast := tt.start - 1

			if tt.coveredFromStart && tt.cdnLastHeight >= start {
				if tt.cdnLastHeight > prefetchLast {
					prefetchLast = tt.cdnLastHeight
				}
				start = tt.cdnLastHeight + 1
			}

			if start != tt.wantStart {
				t.Fatalf("start=%d want %d", start, tt.wantStart)
			}

			updated := prefetchLast != tt.start-1
			if updated != tt.wantPrefetchUpdate {
				t.Fatalf("prefetch update=%v want %v",
					updated, tt.wantPrefetchUpdate)
			}
		})
	}
}

func TestIsVerificationError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "generic error",
			err:  fmt.Errorf("connection refused"),
			want: false,
		},
		{
			name: "HTTP error",
			err:  fmt.Errorf("fetch filters at height 928000: HTTP 500"),
			want: false,
		},
		{
			name: "verification failure",
			err:  fmt.Errorf("import filters at height 928000: filter verification failed at height 928484: computed header abc != stored header def"),
			want: true,
		},
		{
			name: "wrapped verification failure",
			err:  fmt.Errorf("download chunk: %w", fmt.Errorf("filter verification failed at height 100")),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isVerificationError(tt.err)
			if got != tt.want {
				t.Fatalf("isVerificationError(%v) = %v, want %v",
					tt.err, got, tt.want)
			}
		})
	}
}

func TestReadCompactSize(t *testing.T) {
	tests := []struct {
		name      string
		data      []byte
		wantVal   uint64
		wantBytes int
	}{
		{"single byte 0", []byte{0x00}, 0, 1},
		{"single byte 252", []byte{0xfc}, 252, 1},
		{"two byte 253", []byte{0xfd, 0xfd, 0x00}, 253, 3},
		{"two byte 1000", []byte{0xfd, 0xe8, 0x03}, 1000, 3},
		{"four byte", []byte{0xfe, 0x01, 0x00, 0x01, 0x00}, 65537, 5},
		{"empty", []byte{}, 0, 0},
		{"truncated two byte", []byte{0xfd, 0x01}, 0, 0},
		{"truncated four byte", []byte{0xfe, 0x01, 0x02}, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, n := readCompactSize(tt.data)
			if val != tt.wantVal {
				t.Errorf("value = %d, want %d", val, tt.wantVal)
			}
			if n != tt.wantBytes {
				t.Errorf("bytes consumed = %d, want %d", n, tt.wantBytes)
			}
		})
	}
}
