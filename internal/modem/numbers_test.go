package modem

import (
	"github.com/kxn/mobilemod/internal/config"
	"github.com/kxn/mobilemod/internal/domain"
	"github.com/kxn/mobilemod/internal/store"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

func TestNumberBindingsFollowSIMAcrossSwapsAndModules(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Module{ID: "one", Number: "+12025550100"}
	m := New(cfg, db, logger)
	s := domain.Envelope{Protocol: 3, DeviceID: "device-one", ICCID: "sim-one", Capabilities: map[string]bool{"guarded_send": true}}
	if err = m.acceptStatus(s); err != nil {
		t.Fatal(err)
	}
	key := m.Snapshot().SIMKey
	if m.Snapshot().NumberSource != "manual" {
		t.Fatal(m.Snapshot())
	}
	if err = m.SetNumber(key, "+12025550101"); err != nil {
		t.Fatal(err)
	}
	if m.Snapshot().Receiver != "+12025550101" {
		t.Fatal("manual number not live")
	}
	s.ICCID = "sim-two"
	if err = m.acceptStatus(s); err != nil {
		t.Fatal(err)
	}
	if m.Snapshot().Receiver != "one" || m.Snapshot().ManualNumber != "" {
		t.Fatal("old SIM number leaked to new SIM")
	}
	if err = m.SetNumber(key, "+12025550102"); err != ErrSIMChanged {
		t.Fatal("stale UI write accepted", err)
	}
	other := New(config.Module{ID: "two"}, db, logger)
	s.DeviceID = "device-two"
	s.ICCID = "sim-one"
	if err = other.acceptStatus(s); err != nil {
		t.Fatal(err)
	}
	if other.Snapshot().Receiver != "+12025550101" {
		t.Fatal("SIM binding did not move")
	}
	s.Number = "+12025550102"
	other.acceptStatus(s)
	if other.Snapshot().Receiver != s.Number || other.Snapshot().NumberSource != "sim" {
		t.Fatal("SIM number not prioritized")
	}
	if err = other.SetNumber(key, ""); err != nil {
		t.Fatal(err)
	}
	s.Number = ""
	other.acceptStatus(s)
	if other.Snapshot().Receiver != "two" {
		t.Fatal("cleared fallback retained")
	}
	reopened := New(cfg, db, logger)
	s.DeviceID = "device-one"
	reopened.acceptStatus(s)
	if reopened.Snapshot().Receiver != "one" {
		t.Fatal("legacy config re-seeded cleared mapping")
	}
}
