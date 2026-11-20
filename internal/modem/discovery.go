package modem

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kxn/mobilemod/internal/domain"
)

// Discovery owns live ports; module identity comes from the bridge handshake,
// never from a COM number or the order in which devices were plugged in.
type Discovery struct {
	Log      *slog.Logger
	Find     func(deviceID string) (*Module, error)
	List     func() ([]string, error)
	Open     func(string) (io.ReadWriteCloser, error)
	Interval time.Duration
}

func (d *Discovery) Run(ctx context.Context) {
	list := d.List
	if list == nil {
		list = DiscoverPorts
	}
	open := d.Open
	if open == nil {
		open = openSerial
	}
	interval := d.Interval
	if interval == 0 {
		interval = 3 * time.Second
	}
	var mu sync.Mutex
	ports, devices := map[string]bool{}, map[string]bool{}
	var wg sync.WaitGroup
	defer wg.Wait()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		found, err := list()
		if err != nil {
			d.Log.Warn("USB enumeration unavailable")
		}
		for _, port := range found {
			mu.Lock()
			if ports[port] {
				mu.Unlock()
				continue
			}
			ports[port] = true
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { mu.Lock(); delete(ports, port); mu.Unlock() }()
				p, err := open(port)
				if err != nil {
					return
				}
				c := NewConnection(p)
				stop := context.AfterFunc(ctx, c.Close)
				defer stop()
				var selected atomic.Pointer[Module]
				c.OnEvent = func(e domain.Envelope) {
					if m := selected.Load(); m != nil {
						m.received(ctx, c, e)
					}
				}
				readDone := make(chan struct{})
				go func() { defer close(readDone); c.ReadLoop() }()
				defer func() { c.Close(); <-readDone }()
				probe, cancel := context.WithTimeout(ctx, 5*time.Second)
				e, err := c.Call(probe, map[string]any{"cmd": "status"}, false)
				cancel()
				if err != nil || e.Protocol < 2 || e.DeviceID == "" {
					return
				}
				mu.Lock()
				if devices[e.DeviceID] {
					mu.Unlock()
					return
				}
				devices[e.DeviceID] = true
				mu.Unlock()
				defer func() { mu.Lock(); delete(devices, e.DeviceID); mu.Unlock() }()
				m, err := d.Find(e.DeviceID)
				if err != nil {
					d.Log.Warn("cannot register USB module")
					return
				}
				selected.Store(m)
				if err := m.serve(ctx, c, e, port); err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
					d.Log.Info("module disconnected", "module", m.Config.ID)
				}
			}()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
