package neutrino

import (
	"os"
	"testing"
	"time"

	"github.com/btcsuite/btclog"
)

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
				ConnectPeers:    "localhost:18444",
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
}
