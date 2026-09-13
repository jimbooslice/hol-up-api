package scheduleingest

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"hol-up-prototype/internal/schedule"
)

const cacheSchema = 1
const maxCacheBytes = (MaxArchiveBytes+2)/3*4 + 64<<10

var ErrCache = errors.New("invalid schedule cache")

// Cache persists only the active last-known-good archive and its validation time.
type Cache interface {
	Load() (CacheEntry, error)
	Save(CacheEntry) error
}
type CacheEntry struct {
	Schema   int
	Metadata schedule.Metadata
	Archive  []byte
}

type FileCache struct{ Dir string }

func (c FileCache) Load() (CacheEntry, error) {
	f, err := os.Open(filepath.Join(c.Dir, "active.json"))
	if err != nil {
		return CacheEntry{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return CacheEntry{}, err
	}
	if info.Size() > maxCacheBytes {
		return CacheEntry{}, ErrLimit
	}
	bounded := &io.LimitedReader{R: f, N: maxCacheBytes + 1}
	d := json.NewDecoder(bounded)
	d.DisallowUnknownFields()
	var entry CacheEntry
	if err := d.Decode(&entry); err != nil {
		return CacheEntry{}, ErrCache
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return CacheEntry{}, ErrCache
	}
	if bounded.N == 0 || int64(len(entry.Archive)) > MaxArchiveBytes {
		return CacheEntry{}, ErrLimit
	}
	return entry, nil
}

func (c FileCache) Save(entry CacheEntry) error {
	if int64(len(entry.Archive)) > MaxArchiveBytes {
		return ErrLimit
	}
	if err := os.MkdirAll(c.Dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(c.Dir, ".active-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := json.NewEncoder(f).Encode(entry); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(c.Dir, "active.json")); err != nil {
		return err
	}
	dir, err := os.Open(c.Dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
