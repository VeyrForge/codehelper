package cache

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/dgraph-io/badger/v4"
)

// FileCache stores path -> content hash in badger.
type FileCache struct {
	db *badger.DB
}

// OpenFileCache opens badger at dir.
func OpenFileCache(dir string) (*FileCache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	opts := badger.DefaultOptions(filepath.Join(dir, "badger"))
	opts.Logger = nil
	db, err := badger.Open(opts)
	if err != nil {
		return nil, err
	}
	return &FileCache{db: db}, nil
}

// Close releases the database.
func (f *FileCache) Close() error {
	if f.db == nil {
		return nil
	}
	return f.db.Close()
}

// GetHash returns stored hash for path or empty.
func (f *FileCache) GetHash(path string) (string, error) {
	var val string
	err := f.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte("h:" + path))
		if err == badger.ErrKeyNotFound {
			return nil
		}
		if err != nil {
			return err
		}
		return item.Value(func(v []byte) error {
			val = string(v)
			return nil
		})
	})
	return val, err
}

// SetHash stores hash for path.
func (f *FileCache) SetHash(path, hash string) error {
	return f.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte("h:"+path), []byte(hash))
	})
}

// MetaJSON stores small json blob under key.
func (f *FileCache) MetaJSON(key string, v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return f.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte("m:"+key), b)
	})
}

// GetMetaJSON loads json.
func (f *FileCache) GetMetaJSON(key string, dest interface{}) error {
	return f.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte("m:" + key))
		if err == badger.ErrKeyNotFound {
			return nil
		}
		if err != nil {
			return err
		}
		return item.Value(func(v []byte) error {
			return json.Unmarshal(v, dest)
		})
	})
}
