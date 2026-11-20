package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestRollbackAndReopen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Update(func(tx *Tx) error {
		if e := tx.Put("settings", "a", 42); e != nil {
			return e
		}
		return errors.New("abort")
	}); err == nil {
		t.Fatal("expected abort")
	}
	s.View(func(tx *Tx) error {
		if tx.Has("settings", "a") {
			t.Fatal("rollback failed")
		}
		return nil
	})
	if err = s.Update(func(tx *Tx) error { return tx.Put("settings", "b", map[string]int{"value": 7}) }); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.View(func(tx *Tx) error {
		var v map[string]int
		if err := tx.Get("settings", "b", &v); err != nil {
			t.Fatal(err)
		}
		if v["value"] != 7 {
			t.Fatal(v)
		}
		return nil
	})
}
func TestExclusiveWriterAndInvalidKeys(t *testing.T) {
	p := filepath.Join(t.TempDir(), "db")
	s, e := Open(p)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if other, e := Open(p); e == nil {
		other.Close()
		t.Fatal("second writer opened")
	}
	if e = s.Update(func(tx *Tx) error { return tx.Put("settings", "", 1) }); e == nil {
		t.Fatal("empty key")
	}
}
