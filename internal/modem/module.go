package modem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/kxn/mobilemod/internal/config"
	"github.com/kxn/mobilemod/internal/domain"
	"github.com/kxn/mobilemod/internal/phone"
	"github.com/kxn/mobilemod/internal/store"
	"go.bug.st/serial"
)

type Snapshot struct {
	ID             string          `json:"id"`
	Port           string          `json:"port"`
	Online         bool            `json:"online"`
	Receiver       string          `json:"receiver"`
	Core           string          `json:"core"`
	Protocol       int             `json:"protocol"`
	Network        int             `json:"network"`
	CSQ            int             `json:"csq"`
	Ready          bool            `json:"sms_ready"`
	LastSeen       time.Time       `json:"last_seen"`
	Error          string          `json:"error,omitempty"`
	Identity       domain.Identity `json:"-"`
	SIMKey         string          `json:"sim_key"`
	DetectedNumber string          `json:"detected_number"`
	ManualNumber   string          `json:"manual_number"`
	NumberSource   string          `json:"number_source"`
	GuardedSend    bool            `json:"guarded_send"`
	IncomingCalls  bool            `json:"incoming_calls"`
	PassiveCalls   bool            `json:"passive_calls"`
}
type Module struct {
	Config config.Module
	Store  *store.Store
	Log    *slog.Logger
	Handle func(context.Context, domain.Identity, domain.Envelope) error
	Notice func(string, int) error
	// Open is injectable for integration tests and future transports. Nil uses USB serial.
	Open   func(string) (io.ReadWriteCloser, error)
	mu     sync.RWMutex
	conn   *Connection
	status Snapshot
	sendMu sync.Mutex
}

func New(cfg config.Module, db *store.Store, log *slog.Logger) *Module {
	return &Module{Config: cfg, Store: db, Log: log, status: Snapshot{ID: cfg.ID, Port: cfg.Port, Receiver: cfg.ID, NumberSource: "module_id"}}
}
func (m *Module) Snapshot() Snapshot {
	m.mu.RLock()
	s := m.status
	m.mu.RUnlock()
	return m.resolveNumber(s)
}
func simHash(iccid string) string {
	if iccid == "" {
		return "unknown"
	}
	sum := sha256.Sum256([]byte(iccid))
	return hex.EncodeToString(sum[:12])
}
func (m *Module) acceptStatus(e domain.Envelope) error {
	if e.Protocol < 2 || e.DeviceID == "" {
		return errors.New("bridge protocol 2 firmware required")
	}
	if m.Config.ExpectedDevice != "" && m.Config.ExpectedDevice != e.DeviceID {
		return errors.New("physical device does not match expected_device_id")
	}
	if err := m.Store.Update(func(tx *store.Tx) error {
		var existing string
		err := tx.Get("settings", "device:"+m.Config.ID, &existing)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && existing != e.DeviceID {
			return errors.New("module port now identifies a different physical device; explicit rebind required")
		}
		// A device cannot be simultaneously assigned to two logical modules.
		var owner string
		err = tx.Get("settings", "physical:"+e.DeviceID, &owner)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && owner != m.Config.ID {
			return errors.New("physical device already bound to another module id")
		}
		if err = tx.Put("settings", "device:"+m.Config.ID, e.DeviceID); err != nil {
			return err
		}
		if err := tx.Put("settings", "physical:"+e.DeviceID, m.Config.ID); err != nil {
			return err
		}
		return m.seedNumber(tx, simHash(e.ICCID))
	}); err != nil {
		return err
	}
	s := Snapshot{ID: m.Config.ID, Port: m.Config.Port, Online: true, DetectedNumber: e.Number, SIMKey: simHash(e.ICCID), Core: e.Core, Protocol: e.Protocol, Network: e.Network, CSQ: e.CSQ, Ready: e.SMSReady, LastSeen: time.Now(), GuardedSend: e.Protocol >= 3 && e.Capabilities["guarded_send"], IncomingCalls: e.Capabilities["incoming_calls"], PassiveCalls: e.Capabilities["passive_call_listener"]}
	s.Identity = domain.Identity{Module: m.Config.ID, Receiver: s.Receiver, SIM: simHash(e.ICCID)}
	s = m.resolveNumber(s)
	m.mu.Lock()
	m.status = s
	m.mu.Unlock()
	if e.Drops > 0 && m.Notice != nil {
		if err := m.Notice("overflow", e.Drops); err != nil {
			return err
		}
	}
	return nil
}
func (m *Module) received(ctx context.Context, c *Connection, e domain.Envelope) {
	if e.Event == "overflow" {
		m.Log.Error("module storage overflow", "module", m.Config.ID)
		if m.Notice != nil {
			m.Notice("overflow", e.Drops)
		}
		return
	}
	s := m.Snapshot()
	if !s.Online || m.Handle == nil {
		return
	}
	// Capture SIM identity with each event, not just from the latest poll.
	identity := s.Identity
	if !e.Durable && m.Notice != nil {
		m.Notice("volatile_sms", int(e.MessageID))
	}
	if e.ICCID != "" {
		identity.SIM = simHash(e.ICCID)
		actual := e.Number
		if actual == "" && identity.SIM == s.Identity.SIM {
			actual = s.DetectedNumber
		}
		identity.Receiver = m.resolveNumber(Snapshot{DetectedNumber: actual, SIMKey: identity.SIM}).Receiver
	}
	if err := m.Handle(ctx, identity, e); err != nil {
		m.Log.Error("incoming SMS not acknowledged", "module", m.Config.ID, "reason", "persistence failed")
		return
	}
	if err := c.Write(map[string]any{"cmd": "ack", "id": domain.Key("ack", e.BootID, fmt.Sprint(e.MessageID)), "message_id": e.MessageID, "boot_id": e.BootID}); err != nil {
		m.Log.Warn("SMS ack deferred", "module", m.Config.ID)
	}
}
func (m *Module) connect(ctx context.Context) error {
	open := m.Open
	if open == nil {
		open = func(name string) (io.ReadWriteCloser, error) {
			p, e := serial.Open(name, &serial.Mode{BaudRate: 115200, DataBits: 8, Parity: serial.NoParity, StopBits: serial.OneStopBit})
			if e != nil {
				return nil, e
			}
			if e = p.SetReadTimeout(300 * time.Millisecond); e != nil {
				p.Close()
				return nil, e
			}
			return p, nil
		}
	}
	p, err := open(m.Config.Port)
	if err != nil {
		return errors.New("cannot open configured serial port")
	}
	c := NewConnection(p)
	stop := context.AfterFunc(ctx, c.Close)
	defer stop()
	readDone := make(chan struct{})
	defer func() { c.Close(); <-readDone }()
	c.OnEvent = func(e domain.Envelope) { m.received(ctx, c, e) }
	go func() { defer close(readDone); c.ReadLoop() }()
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	e, err := c.Call(qctx, map[string]any{"cmd": "status"}, false)
	cancel()
	if err != nil {
		return err
	}
	if err = m.acceptStatus(e); err != nil {
		return err
	}
	m.mu.Lock()
	m.conn = c
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.conn = nil; m.status.Online = false; m.mu.Unlock() }()
	m.Log.Info("module connected", "module", m.Config.ID, "port", m.Config.Port, "core", e.Core)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		qctx, cancel = context.WithTimeout(ctx, 4*time.Second)
		e, err = c.Call(qctx, map[string]any{"cmd": "status"}, false)
		cancel()
		if err != nil {
			return err
		}
		if err = m.acceptStatus(e); err != nil {
			return err
		}
		qctx, cancel = context.WithTimeout(ctx, 4*time.Second)
		e, err = c.Call(qctx, map[string]any{"cmd": "inbox"}, false)
		cancel()
		if err != nil {
			return err
		}
		for _, msg := range e.Messages {
			m.received(ctx, c, msg)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.Done():
			return ErrDisconnected
		case <-ticker.C:
		}
	}
}
func (m *Module) Run(ctx context.Context) {
	for ctx.Err() == nil {
		err := m.connect(ctx)
		m.mu.Lock()
		m.status.Online = false
		m.status.Error = "disconnected or identity/protocol mismatch"
		m.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		m.Log.Warn("module reconnect scheduled", "module", m.Config.ID, "reason", safeError(err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}
func safeError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "serial/protocol error"
}
func (m *Module) Send(ctx context.Context, id, to, text string, expected domain.Identity) (domain.Envelope, error) {
	m.sendMu.Lock()
	defer m.sendMu.Unlock()
	m.mu.RLock()
	c := m.conn
	m.mu.RUnlock()
	s := m.Snapshot()
	if c == nil || !s.Online {
		return domain.Envelope{}, errors.New("module offline")
	}
	if !s.GuardedSend || expected.SIM == "" || expected.SIM == "unknown" || s.Identity.SIM != expected.SIM || s.Receiver != expected.Receiver {
		return domain.Envelope{}, ErrSIMChanged
	}
	// A fresh status query prevents a stale SIM binding from selecting the sender.
	checkCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	current, err := c.Call(checkCtx, map[string]any{"cmd": "status"}, false)
	cancel()
	if err != nil {
		return domain.Envelope{}, err
	}
	if current.Protocol < 3 || !current.Capabilities["guarded_send"] || !current.SMSReady || current.ICCID == "" || simHash(current.ICCID) != expected.SIM {
		return domain.Envelope{}, ErrSIMChanged
	}
	actual := m.resolveNumber(Snapshot{DetectedNumber: current.Number, SIMKey: simHash(current.ICCID)})
	if actual.Receiver != expected.Receiver {
		return domain.Envelope{}, ErrSIMChanged
	}
	out, err := c.Call(ctx, map[string]any{"cmd": "sms_send", "id": id, "to": phone.Normalize(to), "text": text, "expected_iccid": current.ICCID}, true)
	if out.Event == "error" && out.Error == "SIM changed or expected SIM missing" {
		return out, ErrSIMChanged
	}
	return out, err
}
