// Package store provides synchronous transactional persistence. A single bbolt
// writer lock also prevents two processes from controlling the same deployment.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

var buckets = []string{"raw", "groups", "messages", "outbox", "commands", "sends", "settings", "number_bindings", "replies"}

type Store struct{ db *bolt.DB }
type Tx struct{ tx *bolt.Tx }

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open state database (another instance may be running): %w", err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range buckets {
			if _, e := tx.CreateBucketIfNotExists([]byte(b)); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Update(fn func(*Tx) error) error {
	return s.db.Update(func(tx *bolt.Tx) error { return fn(&Tx{tx}) })
}
func (s *Store) View(fn func(*Tx) error) error {
	return s.db.View(func(tx *bolt.Tx) error { return fn(&Tx{tx}) })
}
func (t *Tx) Has(bucket, key string) bool { return t.tx.Bucket([]byte(bucket)).Get([]byte(key)) != nil }
func (t *Tx) Get(bucket, key string, out any) error {
	v := t.tx.Bucket([]byte(bucket)).Get([]byte(key))
	if v == nil {
		return os.ErrNotExist
	}
	return json.Unmarshal(v, out)
}
func (t *Tx) Put(bucket, key string, value any) error {
	if key == "" {
		return errors.New("empty database key")
	}
	v, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return t.tx.Bucket([]byte(bucket)).Put([]byte(key), v)
}
func (t *Tx) Delete(bucket, key string) error { return t.tx.Bucket([]byte(bucket)).Delete([]byte(key)) }
func (t *Tx) Scan(bucket string, fn func(string, []byte) error) error {
	return t.tx.Bucket([]byte(bucket)).ForEach(func(k, v []byte) error {
		if v == nil {
			return nil
		}
		return fn(string(k), v)
	})
}
func (t *Tx) Count(bucket string) int { return t.tx.Bucket([]byte(bucket)).Stats().KeyN }
