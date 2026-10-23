// Package store provides a tiny JSON-file backed persistence layer.
//
// Every collection is one file mapping ID -> record. The API is deliberately
// narrow so it can be swapped for SQLite later without touching callers.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

type Collection[T any] struct {
	mu    sync.RWMutex
	path  string
	items map[string]T
}

// Open loads the collection at path, creating an empty one if the file does not exist.
func Open[T any](path string) (*Collection[T], error) {
	c := &Collection[T]{path: path, items: map[string]T{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &c.items); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Collection[T]) Get(id string) (T, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.items[id]
	return v, ok
}

// List returns all records ordered by ID.
func (c *Collection[T]) List() []T {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ids := make([]string, 0, len(c.items))
	for id := range c.items {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]T, 0, len(ids))
	for _, id := range ids {
		out = append(out, c.items[id])
	}
	return out
}

func (c *Collection[T]) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}

func (c *Collection[T]) Put(id string, v T) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[id] = v
	return c.save()
}

func (c *Collection[T]) Delete(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, id)
	return c.save()
}

// save writes atomically via a temp file + rename. Caller holds c.mu.
func (c *Collection[T]) save() error {
	data, err := json.MarshalIndent(c.items, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}
