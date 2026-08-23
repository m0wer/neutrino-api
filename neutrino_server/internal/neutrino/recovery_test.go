package neutrino

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/btcsuite/btcd/chainhash/v2"
	"github.com/btcsuite/btcd/wire/v2"
	"github.com/btcsuite/btclog"
	"github.com/btcsuite/btcwallet/walletdb"
)

func TestInspectHeaderCache(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(t *testing.T, dataDir string, db walletdb.DB)
		wantReason string
	}{
		{
			name: "consistent",
		},
		{
			name: "flat files ahead of index",
			mutate: func(t *testing.T, dataDir string, _ walletdb.DB) {
				appendBlockHeader(t, dataDir, wire.BlockHeader{})
				appendFile(t, filepath.Join(dataDir, filterHeaderFileName), make([]byte, filterHeaderSize))
			},
		},
		{
			name: "block index ahead of file",
			mutate: func(t *testing.T, _ string, db walletdb.DB) {
				setHeaderIndexTip(t, db, blockTipKeyName, chainhash.Hash{1}, 1)
			},
			wantReason: "database index tip is height 1 but the file ends at height 0",
		},
		{
			name: "partial block header",
			mutate: func(t *testing.T, dataDir string, _ walletdb.DB) {
				appendFile(t, filepath.Join(dataDir, blockHeaderFileName), []byte{1})
			},
			wantReason: "is not aligned to its 80-byte record size",
		},
		{
			name: "block tip mismatch",
			mutate: func(t *testing.T, _ string, db walletdb.DB) {
				setHeaderIndexTip(t, db, blockTipKeyName, chainhash.Hash{1}, 0)
			},
			wantReason: "does not match block_headers.bin",
		},
		{
			name: "filter index ahead of file",
			mutate: func(t *testing.T, _ string, db walletdb.DB) {
				setHeaderIndexTip(t, db, filterTipKeyName, chainhash.Hash{1}, 1)
			},
			wantReason: "database index tip is height 1 but the file ends at height 0",
		},
		{
			name: "filter index ahead of block index",
			mutate: func(t *testing.T, dataDir string, db walletdb.DB) {
				header := wire.BlockHeader{}
				appendBlockHeader(t, dataDir, header)
				appendFile(
					t, filepath.Join(dataDir, filterHeaderFileName),
					make([]byte, filterHeaderSize),
				)
				setHeaderIndexTip(t, db, filterTipKeyName, header.BlockHash(), 1)
			},
			wantReason: "filter header index tip height 1 is ahead of block header index tip height 0",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataDir := t.TempDir()
			db := createConsistentHeaderCache(t, dataDir)
			if test.mutate != nil {
				test.mutate(t, dataDir, db)
			}

			err := inspectHeaderCache(dataDir, db)
			if test.wantReason == "" {
				if err != nil {
					t.Fatalf("inspectHeaderCache() error = %v", err)
				}
				return
			}

			var inconsistency *headerCacheInconsistency
			if !errors.As(err, &inconsistency) {
				t.Fatalf("expected headerCacheInconsistency, got %v", err)
			}
			if !strings.Contains(err.Error(), test.wantReason) {
				t.Fatalf("error %q does not contain %q", err, test.wantReason)
			}
		})
	}
}

func TestQuarantineHeaderCache(t *testing.T) {
	dataDir := t.TempDir()
	for _, name := range headerCacheFiles {
		writeFile(t, filepath.Join(dataDir, name), []byte(name))
	}
	for _, name := range []string{"rescan_state.db", "auth_token", "tls.cert"} {
		writeFile(t, filepath.Join(dataDir, name), []byte(name))
	}

	now := time.Date(2026, time.August, 23, 16, 30, 0, 123, time.UTC)
	recoveryDir, err := quarantineHeaderCache(dataDir, now)
	if err != nil {
		t.Fatalf("quarantineHeaderCache() error = %v", err)
	}

	wantDir := filepath.Join(dataDir, "header-cache-recovery-20260823T163000.000000123Z")
	if recoveryDir != wantDir {
		t.Fatalf("recovery directory = %q, want %q", recoveryDir, wantDir)
	}
	for _, name := range headerCacheFiles {
		if _, err := os.Stat(filepath.Join(dataDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("cache file %s was not moved", name)
		}
		assertFileContents(t, filepath.Join(recoveryDir, name), []byte(name))
	}
	for _, name := range []string{"rescan_state.db", "auth_token", "tls.cert"} {
		assertFileContents(t, filepath.Join(dataDir, name), []byte(name))
	}
}

func TestOpenChainDatabaseRecoversInconsistentCache(t *testing.T) {
	dataDir := t.TempDir()
	db := createConsistentHeaderCache(t, dataDir)
	setHeaderIndexTip(t, db, blockTipKeyName, chainhash.Hash{1}, 1)
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture database: %v", err)
	}

	backend := btclog.NewBackend(&bytes.Buffer{})
	node := &Node{
		config: &Config{
			DataDir:                dataDir,
			AutoRecoverHeaderCache: true,
		},
		logger: backend.Logger("TEST"),
	}
	dbPath := filepath.Join(dataDir, chainDatabaseFileName)
	if err := node.openChainDatabase(dbPath); err != nil {
		t.Fatalf("openChainDatabase() error = %v", err)
	}
	if err := node.db.Close(); err != nil {
		t.Fatalf("close recovered database: %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(dataDir, "header-cache-recovery-*"))
	if err != nil {
		t.Fatalf("glob recovery directories: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("recovery directories = %v, want one", matches)
	}
	for _, name := range headerCacheFiles {
		if _, err := os.Stat(filepath.Join(matches[0], name)); err != nil {
			t.Errorf("recovered cache file %s: %v", name, err)
		}
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("fresh chain database was not created: %v", err)
	}
}

func TestOpenChainDatabaseCanDisableRecovery(t *testing.T) {
	dataDir := t.TempDir()
	db := createConsistentHeaderCache(t, dataDir)
	setHeaderIndexTip(t, db, blockTipKeyName, chainhash.Hash{1}, 1)
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture database: %v", err)
	}

	backend := btclog.NewBackend(&bytes.Buffer{})
	node := &Node{
		config: &Config{DataDir: dataDir},
		logger: backend.Logger("TEST"),
	}
	err := node.openChainDatabase(filepath.Join(dataDir, chainDatabaseFileName))
	if err == nil || !strings.Contains(err.Error(), "automatic recovery is disabled") {
		t.Fatalf("openChainDatabase() error = %v", err)
	}
	if node.db != nil {
		t.Fatal("database remains open after failed startup")
	}
	if _, err := os.Stat(filepath.Join(dataDir, blockHeaderFileName)); err != nil {
		t.Fatalf("block header file was moved with recovery disabled: %v", err)
	}
}

func createConsistentHeaderCache(t *testing.T, dataDir string) walletdb.DB {
	t.Helper()

	db, err := walletdb.Create(
		"bdb", filepath.Join(dataDir, chainDatabaseFileName), true,
		time.Second, false,
	)
	if err != nil {
		t.Fatalf("create fixture database: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	genesis := chaincfg.SigNetParams.GenesisBlock.Header
	appendBlockHeader(t, dataDir, genesis)
	writeFile(
		t, filepath.Join(dataDir, filterHeaderFileName),
		make([]byte, filterHeaderSize),
	)
	genesisHash := genesis.BlockHash()
	setHeaderIndexTip(t, db, blockTipKeyName, genesisHash, 0)
	setHeaderIndexTip(t, db, filterTipKeyName, genesisHash, 0)
	return db
}

func setHeaderIndexTip(t *testing.T, db walletdb.DB, key string,
	hash chainhash.Hash, height uint32) {

	t.Helper()
	err := walletdb.Update(db, func(tx walletdb.ReadWriteTx) error {
		root := tx.ReadWriteBucket([]byte(headerIndexBucketName))
		if root == nil {
			var err error
			root, err = tx.CreateTopLevelBucket([]byte(headerIndexBucketName))
			if err != nil {
				return err
			}
		}

		bucket := root.NestedReadWriteBucket(hash[:2])
		if bucket == nil {
			var err error
			bucket, err = root.CreateBucket(hash[:2])
			if err != nil {
				return err
			}
		}
		var heightBytes [4]byte
		binary.BigEndian.PutUint32(heightBytes[:], height)
		if err := bucket.Put(hash[:], heightBytes[:]); err != nil {
			return err
		}
		return root.Put([]byte(key), hash[:])
	})
	if err != nil {
		t.Fatalf("set header index tip: %v", err)
	}
}

func appendBlockHeader(t *testing.T, dataDir string, header wire.BlockHeader) {
	t.Helper()

	var buffer bytes.Buffer
	if err := header.Serialize(&buffer); err != nil {
		t.Fatalf("serialize block header: %v", err)
	}
	appendFile(t, filepath.Join(dataDir, blockHeaderFileName), buffer.Bytes())
}

func appendFile(t *testing.T, path string, data []byte) {
	t.Helper()

	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("open fixture file: %v", err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		t.Fatalf("append fixture file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close fixture file: %v", err)
	}
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
}

func assertFileContents(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("read %s: %v", path, err)
		return
	}
	if !bytes.Equal(got, want) {
		t.Errorf("contents of %s = %q, want %q", path, got, want)
	}
}
