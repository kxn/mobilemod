package modem

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kxn/mobilemod/internal/config"
	"github.com/kxn/mobilemod/internal/domain"
	"github.com/kxn/mobilemod/internal/store"
)

func TestDiscoveryEmptyStartupTwoDevicesUnplugAndChangedPort(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var mu sync.Mutex
	available := map[string]string{}
	peers := map[string]net.Conn{}
	modules := map[string]*Module{}
	d := &Discovery{Log: log, Interval: 10 * time.Millisecond}
	d.List = func() ([]string, error) {
		mu.Lock()
		defer mu.Unlock()
		var ports []string
		for port := range available {
			ports = append(ports, port)
		}
		return ports, nil
	}
	d.Find = func(id string) (*Module, error) {
		mu.Lock()
		defer mu.Unlock()
		m := modules[id]
		if m == nil {
			m = New(config.Module{ID: id}, db, log)
			modules[id] = m
		}
		return m, nil
	}
	d.Open = func(port string) (io.ReadWriteCloser, error) {
		mu.Lock()
		id, ok := available[port]
		if !ok {
			mu.Unlock()
			return nil, io.EOF
		}
		host, device := net.Pipe()
		peers[port] = device
		mu.Unlock()
		go func() {
			defer device.Close()
			scan := bufio.NewScanner(device)
			for scan.Scan() {
				var q map[string]any
				json.Unmarshal(scan.Bytes(), &q)
				e := domain.Envelope{ID: q["id"].(string), Event: "status", Protocol: 3, DeviceID: id, ICCID: "sim-" + id, SMSReady: true}
				if q["cmd"] == "inbox" {
					e.Event = "inbox"
				}
				b, _ := json.Marshal(e)
				if _, err := device.Write(append(b, '\n')); err != nil {
					return
				}
			}
		}()
		return host, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); d.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("discovery did not stop")
		}
	}()
	await := func(predicate func() bool) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for !predicate() {
			if time.Now().After(deadline) {
				t.Fatal("hotplug state did not converge")
			}
			time.Sleep(time.Millisecond)
		}
	}
	// No port at startup: the manager stays alive and waits for insertion.
	select {
	case <-done:
		t.Fatal("empty startup exited")
	default:
	}
	mu.Lock()
	available["first-port"] = "device-a"
	available["second-port"] = "device-b"
	mu.Unlock()
	await(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(modules) == 2 && modules["device-a"].Snapshot().Online && modules["device-b"].Snapshot().Online
	})
	mu.Lock()
	original := modules["device-a"]
	peer := peers["first-port"]
	delete(available, "first-port")
	mu.Unlock()
	peer.Close()
	await(func() bool { return !original.Snapshot().Online })
	if original.Snapshot().Ready {
		t.Fatal("unplugged module still SMS-ready")
	}
	mu.Lock()
	available["renumbered-port"] = "device-a"
	mu.Unlock()
	await(func() bool { s := original.Snapshot(); return s.Online && s.Port == "renumbered-port" })
	mu.Lock()
	same := modules["device-a"] == original
	count := len(modules)
	other := modules["device-b"].Snapshot().Online
	mu.Unlock()
	if !same || count != 2 || !other {
		t.Fatal("identity changed or another module was disrupted")
	}
}
