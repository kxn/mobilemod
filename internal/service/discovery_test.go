package service

import (
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/kxn/mobilemod/internal/config"
	"github.com/kxn/mobilemod/internal/store"
)

func TestDynamicModulesReuseIdentityAndExistingBindings(t *testing.T) {
	cfg := runtimeConfig(t)
	cfg.AutoDiscover = true
	cfg.Modules = nil
	r, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Store.Close()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, id := range []string{"device-a", "device-b"} {
				if _, err := r.findModule(id); err != nil {
					t.Error(err)
				}
				r.Engine.devices()
				if _, err := r.AdminStatus(); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	if len(r.modules()) != 2 || len(r.Engine.devices()) != 2 {
		t.Fatal("duplicate automatic module records")
	}
	first, err := r.findModule("device-a")
	if err != nil {
		t.Fatal(err)
	}
	again, err := r.findModule("device-a")
	if err != nil || again != first {
		t.Fatal("device identity not reused")
	}
	// A device bound by an older release keeps its existing module name.
	if err := r.Store.Update(func(tx *store.Tx) error { return tx.Put("settings", "physical:old-device", "previous-name") }); err != nil {
		t.Fatal(err)
	}
	old, err := r.findModule("old-device")
	if err != nil || old.Config.ID != "previous-name" {
		t.Fatal("existing binding lost")
	}
	// Explicit aliases can bind by device identity without naming any port.
	r.modulesMu.Lock()
	alias := r.addModule(config.Module{ID: "named-module", ExpectedDevice: "named-device"})
	r.modulesMu.Unlock()
	named, err := r.findModule("named-device")
	if err != nil || named != alias {
		t.Fatal("device alias not reused")
	}
}
