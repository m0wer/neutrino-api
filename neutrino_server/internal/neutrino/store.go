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
	// bucketTxHistory stores confirmed watched-transaction records so clients
	// can reconstruct wallet history. Keys are BigEndian(height) || txid, so a
	// cursor Seek gives records above a given height in chronological order.
	bucketTxHistory = []byte("tx_history")

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
		for _, bucket := range [][]byte{bucketWatchedAddrs, bucketUTXOSet, bucketRescanMeta, bucketTxHistory} {
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

// RemoveWatchedAddresses atomically removes watched addresses and UTXOs that
// belong to them. Rescan metadata and confirmed transaction history are left
// untouched.
func (s *StateStore) RemoveWatchedAddresses(addresses map[string]struct{}) (WatchAddressRemoval, error) {
	if len(addresses) == 0 {
		return WatchAddressRemoval{}, nil
	}

	var removal WatchAddressRemoval
	err := s.db.Update(func(tx *bolt.Tx) error {
		watchedAddrs := tx.Bucket(bucketWatchedAddrs)
		utxos := tx.Bucket(bucketUTXOSet)

		for addr := range addresses {
			if watchedAddrs.Get([]byte(addr)) == nil {
				continue
			}
			if err := watchedAddrs.Delete([]byte(addr)); err != nil {
				return fmt.Errorf("failed to remove watched address %s: %w", addr, err)
			}
			removal.RemovedAddresses++
		}

		var keysToDelete [][]byte
		if err := utxos.ForEach(func(key, value []byte) error {
			var utxo UTXO
			if err := json.Unmarshal(value, &utxo); err != nil {
				return fmt.Errorf("failed to unmarshal UTXO %s: %w", string(key), err)
			}
			if _, remove := addresses[utxo.Address]; !remove {
				return nil
			}
			keyCopy := make([]byte, len(key))
			copy(keyCopy, key)
			keysToDelete = append(keysToDelete, keyCopy)
			return nil
		}); err != nil {
			return err
		}

		for _, key := range keysToDelete {
			if err := utxos.Delete(key); err != nil {
				return fmt.Errorf("failed to remove UTXO %s: %w", string(key), err)
			}
			removal.RemovedUTXOs++
		}
		return nil
	})
	if err != nil {
		return WatchAddressRemoval{}, fmt.Errorf("failed to remove watched addresses: %w", err)
	}
	return removal, nil
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

// --- Transaction History ---

// SaveTxHistory persists a confirmed watched-transaction record. Idempotent:
// re-saving the same (height, txid) overwrites the record.
func (s *StateStore) SaveTxHistory(rec TxHistoryRecord) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketTxHistory)
		data, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("failed to marshal tx history %s: %w", rec.TxID, err)
		}
		return b.Put(txHistoryKey(rec.Height, rec.TxID), data)
	})
}

// SaveTxHistoryBatch persists many records in a single transaction.
func (s *StateStore) SaveTxHistoryBatch(recs []TxHistoryRecord) error {
	if len(recs) == 0 {
		return nil
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketTxHistory)
		for _, rec := range recs {
			data, err := json.Marshal(rec)
			if err != nil {
				return fmt.Errorf("failed to marshal tx history %s: %w", rec.TxID, err)
			}
			if err := b.Put(txHistoryKey(rec.Height, rec.TxID), data); err != nil {
				return err
			}
		}
		return nil
	})
}

// LoadTxHistorySince returns confirmed records with height strictly greater
// than sinceHeight, in ascending (height, txid) order.
func (s *StateStore) LoadTxHistorySince(sinceHeight int32) ([]TxHistoryRecord, error) {
	var out []TxHistoryRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketTxHistory).Cursor()
		seek := make([]byte, 4)
		// Records are keyed by height; seek to the first key at height+1 so
		// the result set is strictly above sinceHeight.
		binary.BigEndian.PutUint32(seek, uint32(sinceHeight+1))
		for k, v := c.Seek(seek); k != nil; k, v = c.Next() {
			var rec TxHistoryRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				s.logger.Warnf("Skipping corrupt tx history entry %x: %v", k, err)
				continue
			}
			out = append(out, rec)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to load tx history: %w", err)
	}
	return out, nil
}

// DeleteTxHistoryFrom removes confirmed records at or above fromHeight. Used on
// a reorg re-scan so stale records for orphaned blocks do not linger.
func (s *StateStore) DeleteTxHistoryFrom(fromHeight int32) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketTxHistory)
		c := b.Cursor()
		seek := make([]byte, 4)
		binary.BigEndian.PutUint32(seek, uint32(fromHeight))
		var keys [][]byte
		for k, _ := c.Seek(seek); k != nil; k, _ = c.Next() {
			key := make([]byte, len(k))
			copy(key, k)
			keys = append(keys, key)
		}
		for _, k := range keys {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

// txHistoryKey builds the bbolt key BigEndian(height) || txid so records sort
// by height first (enabling cursor range scans) then by txid.
func txHistoryKey(height int32, txid string) []byte {
	key := make([]byte, 4+len(txid))
	binary.BigEndian.PutUint32(key[:4], uint32(height))
	copy(key[4:], txid)
	return key
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

// ClearPrivacyData removes watched addresses, UTXOs, and rescan metadata
// while leaving the underlying database intact (block headers and compact
// filters live in a separate neutrino.db file and are not affected).
// This is used by --reset-auth to ensure a fresh start without leaking
// previously watched address information to a new client.
func (s *StateStore) ClearPrivacyData() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		for _, bucket := range [][]byte{bucketWatchedAddrs, bucketUTXOSet, bucketRescanMeta, bucketTxHistory} {
			if err := tx.DeleteBucket(bucket); err != nil {
				return fmt.Errorf("failed to delete bucket %s: %w", string(bucket), err)
			}
			if _, err := tx.CreateBucket(bucket); err != nil {
				return fmt.Errorf("failed to recreate bucket %s: %w", string(bucket), err)
			}
		}
		return nil
	})
}

// LoadRescanMeta returns the last scanned tip and start height.
// Returns (0, 0) if no metadata has been saved yet.
func (s *StateStore) LoadRescanMeta() (lastScannedTip, lastStartHeight int32, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketRescanMeta)

		if tipBytes := b.Get(keyLastScannedTip); len(tipBytes) == 4 {
			lastScannedTip = int32(binary.BigEndian.Uint32(tipBytes))
		}

		if startBytes := b.Get(keyLastStartHeight); len(startBytes) == 4 {
			lastStartHeight = int32(binary.BigEndian.Uint32(startBytes))
		}

		return nil
	})
	if err != nil {
		return 0, 0, fmt.Errorf("failed to load rescan metadata: %w", err)
	}
	return lastScannedTip, lastStartHeight, nil
}
