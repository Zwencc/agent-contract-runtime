package main

import (
	"fmt"
	"os"
	"sync"

	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/memory"
	"github.com/agent-contract-runtime/acr/internal/projector"
)

// leaveStoresOpen is set by main: a hook process exits right after its work,
// and leaving its databases open lets the exit release them. SQLite treats
// that like a crash, which is safe for committed transactions, and so skips
// the checkpoint a close would run and keeps the WAL for the next process,
// which otherwise had to recreate it on its first write. Tests keep closing.
var leaveStoresOpen bool

// storeCache keeps databases open across the daemon's requests. Each hook
// process opening and closing SQLite pays for a fresh WAL on its first write
// and a checkpoint on close, several disk flushes per hook; an open database
// pays only for its commits. It is nil outside the daemon.
var storeCache *handleCache

type cachedHandle struct {
	info  os.FileInfo
	value any
	close func() error
}

type handleCache struct {
	mutex   sync.Mutex
	handles map[string]cachedHandle
}

func newHandleCache() *handleCache { return &handleCache{handles: map[string]cachedHandle{}} }

func (cache *handleCache) closeAll() {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	for key, handle := range cache.handles {
		handle.close()
		delete(cache.handles, key)
	}
}

// openCached opens path, or in the daemon reuses the handle opened before,
// unless the file was replaced since. release closes what it opened outside
// the daemon and does nothing inside it.
func openCached[T any](kind, path string, open func(string) (T, error), closeValue func(T) error) (T, func(), error) {
	if storeCache == nil {
		value, err := open(path)
		if err != nil || leaveStoresOpen {
			return value, func() {}, err
		}
		return value, func() { closeValue(value) }, nil
	}
	cache := storeCache
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	key := kind + ":" + path
	current, statErr := os.Stat(path)
	if handle, ok := cache.handles[key]; ok {
		if statErr == nil && os.SameFile(handle.info, current) {
			return handle.value.(T), func() {}, nil
		}
		handle.close()
		delete(cache.handles, key)
	}
	value, err := open(path)
	if err != nil {
		return value, func() {}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		closeValue(value)
		var zero T
		return zero, func() {}, fmt.Errorf("inspect %s: %w", path, err)
	}
	cache.handles[key] = cachedHandle{info: info, value: value, close: func() error { return closeValue(value) }}
	return value, func() {}, nil
}

func openEvents(path string) (*eventstore.Store, func(), error) {
	return openCached("events", path, eventstore.Open, (*eventstore.Store).Close)
}

func openMemories(path string) (*memory.Store, func(), error) {
	return openCached("memories", path, memory.Open, (*memory.Store).Close)
}

func openProjections(path string) (*projector.Store, func(), error) {
	return openCached("projections", path, projector.Open, (*projector.Store).Close)
}
