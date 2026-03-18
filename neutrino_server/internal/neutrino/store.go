/*
Package neutrino provides UTXO scanning using compact block filters.

store.go implements bbolt-based persistence for the RescanManager state.
This allows watched addresses, the UTXO set, and the last scanned tip
to survive server restarts, avoiding expensive full rescans.

The store uses a separate database file (rescan_state.db) to keep our
application state decoupled from the neutrino library's internal database.
*/
package neutrino

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/btcsuite/btclog"
	bolt "go.etcd.io/bbolt"
)

var (
	// Bucket names for the state database.
	bucketWatchedAddrs = []byte("watched_addrs")
	bucketUTXOSet      = []byte("utxo_set")
	bucketRescanMeta   = []byte("rescan_meta")

	// Keys within the rescan_meta bucket.
	keyLastScannedTip  = []byte("last_scanned_tip")
	keyLastStartHeight = []byte("last_start_height")
)

// StateStore provides bbolt-backed persistence for rescan state.
type StateStore struct {
	db     *bolt.DB
	logger btclog.Logger
}

// OpenStateStore opens (or creates) the state database in the given directory.
func OpenStateStore(dataDir string, logger btclog.Logger) (*StateStore, error) {
	dbPath := filepath.Join(dataDir, "rescan_state.db")
	logger.Infof("Opening rescan state database: %s", dbPath)

	db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("failed to open state database at %s: %w", dbPath, err)
	}

	// Create buckets if they don't exist.
	err = db.Update(func(tx *bolt.Tx) error {
		for _, bucket := range [][]byte{bucketWatchedAddrs, bucketUTXOSet, bucketRescanMeta} {
			if _, err := tx.CreateBucketIfNotExists(bucket); err != nil {
				return fmt.Errorf("failed to create bucket %s: %w", string(bucket), err)
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize state database: %w", err)
	}

	logger.Info("Rescan state database opened successfully")
	return &StateStore{db: db, logger: logger}, nil
}

// Close closes the state database.
func (s *StateStore) Close() error {
	if s.db != nil {
		s.logger.Info("Closing rescan state database")
		return s.db.Close()
	}
	return nil
}

// --- Watched Addresses ---

// SaveWatchedAddr persists a single watched address.
func (s *StateStore) SaveWatchedAddr(addr string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketWatchedAddrs)
		return b.Put([]byte(addr), []byte{})
	})
}

// LoadWatchedAddrs returns all persisted watched addresses.
func (s *StateStore) LoadWatchedAddrs() ([]string, error) {
	var addrs []string
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketWatchedAddrs)
		return b.ForEach(func(k, _ []byte) error {
			addrs = append(addrs, string(k))
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("failed to load watched addresses: %w", err)
	}
	return addrs, nil
}

// --- UTXO Set ---

// SaveUTXOSet persists the entire UTXO set atomically.
// This replaces all existing UTXOs in the bucket.
func (s *StateStore) SaveUTXOSet(utxos map[string]UTXO) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		// Delete and recreate the bucket for a clean replace.
		if err := tx.DeleteBucket(bucketUTXOSet); err != nil {
			return fmt.Errorf("failed to clear utxo bucket: %w", err)
		}
		b, err := tx.CreateBucket(bucketUTXOSet)
		if err != nil {
			return fmt.Errorf("failed to recreate utxo bucket: %w", err)
		}

		for key, utxo := range utxos {
			data, err := json.Marshal(utxo)
			if err != nil {
				return fmt.Errorf("failed to marshal UTXO %s: %w", key, err)
			}
			if err := b.Put([]byte(key), data); err != nil {
				return fmt.Errorf("failed to save UTXO %s: %w", key, err)
			}
		}
		return nil
	})
}

// LoadUTXOSet returns all persisted UTXOs.
func (s *StateStore) LoadUTXOSet() (map[string]UTXO, error) {
	utxos := make(map[string]UTXO)
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketUTXOSet)
		return b.ForEach(func(k, v []byte) error {
			var utxo UTXO
			if err := json.Unmarshal(v, &utxo); err != nil {
				s.logger.Warnf("Skipping corrupt UTXO entry %s: %v", string(k), err)
				return nil // Skip corrupt entries rather than failing entirely
			}
			utxos[string(k)] = utxo
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("failed to load UTXO set: %w", err)
	}
	return utxos, nil
}

// --- Rescan Metadata ---

// SaveRescanMeta persists the last scanned tip and start height.
func (s *StateStore) SaveRescanMeta(lastScannedTip, lastStartHeight int32) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketRescanMeta)

		tipBytes := make([]byte, 4)
		binary.BigEndian.PutUint32(tipBytes, uint32(lastScannedTip))
		if err := b.Put(keyLastScannedTip, tipBytes); err != nil {
			return err
		}

		startBytes := make([]byte, 4)
		binary.BigEndian.PutUint32(startBytes, uint32(lastStartHeight))
		return b.Put(keyLastStartHeight, startBytes)
	})
}

// LoadRescanMeta returns the last scanned tip and start height.
// Returns (0, 0) if no metadata has been saved yet.
func (s *StateStore) LoadRescanMeta() (lastScannedTip, lastStartHeight int32, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketRescanMeta)

		if tipBytes := b.Get(keyLastScannedTip); tipBytes != nil && len(tipBytes) == 4 {
			lastScannedTip = int32(binary.BigEndian.Uint32(tipBytes))
		}

		if startBytes := b.Get(keyLastStartHeight); startBytes != nil && len(startBytes) == 4 {
			lastStartHeight = int32(binary.BigEndian.Uint32(startBytes))
		}

		return nil
	})
	if err != nil {
		return 0, 0, fmt.Errorf("failed to load rescan metadata: %w", err)
	}
	return lastScannedTip, lastStartHeight, nil
}
