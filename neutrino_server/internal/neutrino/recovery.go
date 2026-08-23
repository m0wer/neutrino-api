package neutrino

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcwallet/walletdb"
)

const (
	headerIndexBucketName = "header-index"
	blockTipKeyName       = "bitcoin"
	filterTipKeyName      = "regular"
	blockHeaderFileName   = "block_headers.bin"
	filterHeaderFileName  = "reg_filter_headers.bin"
	chainDatabaseFileName = "neutrino.db"
	blockHeaderSize       = 80
	filterHeaderSize      = 32
)

var headerCacheFiles = []string{
	blockHeaderFileName,
	filterHeaderFileName,
	chainDatabaseFileName,
}

type headerCacheInconsistency struct {
	reason string
}

func (e *headerCacheInconsistency) Error() string {
	return e.reason
}

type headerIndexTip struct {
	hash   []byte
	height uint32
}

// inspectHeaderCache verifies the invariants that upstream Neutrino assumes
// before it reconciles its database index with the flat header files.
func inspectHeaderCache(dataDir string, db walletdb.DB) error {
	blockTip, err := readHeaderIndexTip(db, blockTipKeyName)
	if err != nil {
		return fmt.Errorf("inspect block header index: %w", err)
	}
	if err := inspectHeaderFile(
		dataDir, blockHeaderFileName, blockHeaderSize, blockTip,
	); err != nil {
		return err
	}

	filterTip, err := readHeaderIndexTip(db, filterTipKeyName)
	if err != nil {
		return fmt.Errorf("inspect filter header index: %w", err)
	}
	if err := inspectHeaderFile(
		dataDir, filterHeaderFileName, filterHeaderSize, filterTip,
	); err != nil {
		return err
	}
	if blockTip != nil && filterTip != nil && filterTip.height > blockTip.height {
		return inconsistentHeaderCache(
			"filter header index tip height %d is ahead of block header index tip height %d",
			filterTip.height, blockTip.height,
		)
	}

	if blockTip != nil {
		blockHash, err := readBlockHashAtHeight(dataDir, blockTip.height)
		if err != nil {
			return fmt.Errorf("read indexed block header: %w", err)
		}
		if !bytes.Equal(blockHash, blockTip.hash) {
			return inconsistentHeaderCache(
				"block header index tip at height %d does not match %s",
				blockTip.height, blockHeaderFileName,
			)
		}
	}

	// The filter index tip is a block hash, not a filter hash. Verify it
	// against the corresponding block header after both file sizes pass.
	if filterTip != nil {
		blockHash, err := readBlockHashAtHeight(dataDir, filterTip.height)
		if err != nil {
			return inconsistentHeaderCache(
				"filter header index tip at height %d has no corresponding block header: %v",
				filterTip.height, err,
			)
		}
		if !bytes.Equal(blockHash, filterTip.hash) {
			return inconsistentHeaderCache(
				"filter header index tip at height %d does not match the block header chain",
				filterTip.height,
			)
		}
	}

	return nil
}

func readHeaderIndexTip(db walletdb.DB, tipKey string) (*headerIndexTip, error) {
	var tip *headerIndexTip
	err := walletdb.View(db, func(tx walletdb.ReadTx) error {
		root := tx.ReadBucket([]byte(headerIndexBucketName))
		if root == nil {
			return nil
		}

		hash := root.Get([]byte(tipKey))
		if hash == nil {
			return nil
		}
		if len(hash) != 32 {
			return inconsistentHeaderCache(
				"%s header index tip hash has length %d, expected 32",
				tipKey, len(hash),
			)
		}

		heightBytes := root.Get(hash)
		if bucket := root.NestedReadBucket(hash[:2]); bucket != nil {
			if nestedHeight := bucket.Get(hash); nestedHeight != nil {
				heightBytes = nestedHeight
			}
		}
		if len(heightBytes) != 4 {
			return inconsistentHeaderCache(
				"%s header index tip has no valid height", tipKey,
			)
		}

		tip = &headerIndexTip{
			hash:   bytes.Clone(hash),
			height: binary.BigEndian.Uint32(heightBytes),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return tip, nil
}

func inspectHeaderFile(dataDir, name string, recordSize int64,
	tip *headerIndexTip) error {

	path := filepath.Join(dataDir, name)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		if tip == nil {
			return nil
		}
		return inconsistentHeaderCache(
			"%s is missing but its database index tip is height %d",
			name, tip.height,
		)
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return inconsistentHeaderCache("%s is a directory", name)
	}
	if info.Size()%recordSize != 0 {
		return inconsistentHeaderCache(
			"%s size %d is not aligned to its %d-byte record size",
			name, info.Size(), recordSize,
		)
	}
	if tip == nil {
		if info.Size() == 0 {
			return nil
		}
		return inconsistentHeaderCache(
			"%s contains data but its database index has no tip", name,
		)
	}

	recordCount := uint64(info.Size() / recordSize)
	if uint64(tip.height) >= recordCount {
		return inconsistentHeaderCache(
			"%s database index tip is height %d but the file ends at height %d",
			name, tip.height, int64(recordCount)-1,
		)
	}

	return nil
}

func readBlockHashAtHeight(dataDir string, height uint32) ([]byte, error) {
	path := filepath.Join(dataDir, blockHeaderFileName)
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	rawHeader := make([]byte, blockHeaderSize)
	if _, err := file.ReadAt(rawHeader, int64(height)*blockHeaderSize); err != nil {
		return nil, err
	}

	var header wire.BlockHeader
	if err := header.Deserialize(bytes.NewReader(rawHeader)); err != nil {
		return nil, err
	}
	hash := header.BlockHash()
	return hash.CloneBytes(), nil
}

func inconsistentHeaderCache(format string, args ...any) error {
	return &headerCacheInconsistency{reason: fmt.Sprintf(format, args...)}
}

// quarantineHeaderCache atomically renames each rebuildable cache file into a
// timestamped directory. Header files move first so an interrupted quarantine
// is still detected as inconsistent against the old database on next startup.
func quarantineHeaderCache(dataDir string, now time.Time) (string, error) {
	recoveryDir := filepath.Join(
		dataDir,
		"header-cache-recovery-"+now.UTC().Format("20060102T150405.000000000Z"),
	)
	if err := os.Mkdir(recoveryDir, 0700); err != nil {
		return "", fmt.Errorf("create recovery directory: %w", err)
	}
	if err := syncDirectory(dataDir); err != nil {
		_ = os.Remove(recoveryDir)
		return "", fmt.Errorf("sync recovery directory creation: %w", err)
	}

	moved := make([]string, 0, len(headerCacheFiles))
	for _, name := range headerCacheFiles {
		source := filepath.Join(dataDir, name)
		destination := filepath.Join(recoveryDir, name)
		if err := os.Rename(source, destination); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", rollbackQuarantine(
				dataDir, recoveryDir, moved,
				fmt.Errorf("move %s into recovery directory: %w", name, err),
			)
		}
		moved = append(moved, name)
	}

	if len(moved) == 0 {
		if err := os.Remove(recoveryDir); err != nil {
			return "", fmt.Errorf("remove empty recovery directory: %w", err)
		}
		return "", errors.New("no header cache files exist to quarantine")
	}
	if err := syncDirectory(recoveryDir); err != nil {
		return "", rollbackQuarantine(
			dataDir, recoveryDir, moved,
			fmt.Errorf("sync recovery directory: %w", err),
		)
	}
	if err := syncDirectory(dataDir); err != nil {
		return "", rollbackQuarantine(
			dataDir, recoveryDir, moved,
			fmt.Errorf("sync data directory after quarantine: %w", err),
		)
	}

	return recoveryDir, nil
}

func syncDirectory(path string) error {
	// Go does not provide a portable way to flush directory metadata on
	// Windows. Renames remain atomic there, and the preflight detects an
	// interrupted multi-file quarantine on the next startup.
	if runtime.GOOS == "windows" {
		return nil
	}

	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func rollbackQuarantine(dataDir, recoveryDir string, moved []string,
	cause error) error {

	errs := []error{cause}
	for i := len(moved) - 1; i >= 0; i-- {
		name := moved[i]
		if err := os.Rename(
			filepath.Join(recoveryDir, name), filepath.Join(dataDir, name),
		); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", name, err))
		}
	}
	if err := os.Remove(recoveryDir); err != nil {
		errs = append(errs, fmt.Errorf("remove recovery directory: %w", err))
	}
	if err := syncDirectory(dataDir); err != nil {
		errs = append(errs, fmt.Errorf("sync quarantine rollback: %w", err))
	}

	return errors.Join(errs...)
}
