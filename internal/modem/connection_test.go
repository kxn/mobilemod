package modem

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"github.com/kxn/mobilemod/internal/config"
	"github.com/kxn/mobilemod/internal/domain"
	"github.com/kxn/mobilemod/internal/store"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCancellationUnblocksStalledWrite(t *testing.T) {
	host, device := net.Pipe()
	defer device.Close() // The peer intentionally never reads.
	c := NewConnection(host)
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.Call(ctx, map[string]any{"cmd": "status"}, false); done <- err }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stalled write succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not release serial writer")
	}
}

func TestFragmentedFramesCorrelationAndSendResult(t *testing.T) {
	host, device := net.Pipe()
	c := NewConnection(host)
	defer c.Close()
	events := make(chan domain.Envelope, 2)
	c.OnEvent = func(e domain.Envelope) { events <- e }
	go c.ReadLoop()
	go func() {
		defer device.Close()
		s := bufio.NewScanner(device)
		for s.Scan() {
			var q map[string]any
			json.Unmarshal(s.Bytes(), &q)
			id := q["id"].(string)
			b, _ := json.Marshal(map[string]any{"event": "sms_accepted", "id": id})
			device.Write(append(b, '\n'))
			raw := []byte(`{"event":"sms_received","boot_id":"b","message_id":1,"text":"中文"}` + "\n")
			device.Write(raw[:17])
			device.Write(raw[17:])
			b, _ = json.Marshal(map[string]any{"event": "sms_result", "id": id, "submitted": true})
			device.Write(append(b, '\n'))
			return
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	e, err := c.Call(ctx, map[string]any{"cmd": "sms_send", "id": "test"}, true)
	if err != nil || e.Submitted == nil || !*e.Submitted {
		t.Fatal(e, err)
	}
	select {
	case e = <-events:
		if e.Text != "中文" {
			t.Fatal(e)
		}
	case <-ctx.Done():
		t.Fatal("lost event")
	}
}

// The transport can deliver a final response and close before Write returns.
// A known outcome must survive either a write error or simultaneous EOF.
func TestFinalResponseSurvivesTransportClose(t *testing.T) {
	for _, writeError := range []bool{false, true} {
		for _, rejected := range []bool{false, true} {
			for i := 0; i < 64; i++ {
				p := &replyThenClosePort{}
				c := NewConnection(p)
				p.write = func() {
					c.mu.Lock()
					w := c.waiters["request"]
					c.mu.Unlock()
					submitted := true
					e := domain.Envelope{Event: "sms_result", Submitted: &submitted}
					if rejected {
						e.Event, e.Error = "error", "SIM changed"
					}
					w.ch <- e
					c.Close()
				}
				p.fail = writeError
				e, err := c.Call(context.Background(), map[string]any{"id": "request"}, true)
				if rejected {
					if e.Event != "error" || err == nil || errors.Is(err, ErrDisconnected) {
						t.Fatalf("lost explicit rejection: event=%s err=%v", e.Event, err)
					}
				} else if err != nil || e.Submitted == nil || !*e.Submitted {
					t.Fatalf("lost confirmed submission: event=%s err=%v", e.Event, err)
				}
			}
		}
	}
}

type replyThenClosePort struct {
	write func()
	fail  bool
}

func (p *replyThenClosePort) Read([]byte) (int, error) { return 0, io.EOF }
func (p *replyThenClosePort) Close() error             { return nil }
func (p *replyThenClosePort) Write(b []byte) (int, error) {
	p.write()
	if p.fail {
		return len(b), io.EOF
	}
	return len(b), nil
}

func TestTimeoutDisconnectAndInvalidJSON(t *testing.T) {
	host, device := net.Pipe()
	c := NewConnection(host)
	go c.ReadLoop()
	go func() { buf := make([]byte, 1024); device.Read(buf) }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.Call(ctx, map[string]any{"cmd": "status"}, false)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	device.Write([]byte("garbage\n"))
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("invalid frame not disconnected")
	}
	device.Close()
	if e := c.Write(map[string]string{"cmd": "x"}); e == nil {
		t.Fatal("write after close")
	}
}
func TestConcurrentCalls(t *testing.T) {
	h, d := net.Pipe()
	c := NewConnection(h)
	go c.ReadLoop()
	defer c.Close()
	go func() {
		s := bufio.NewScanner(d)
		for s.Scan() {
			var q map[string]any
			json.Unmarshal(s.Bytes(), &q)
			b, _ := json.Marshal(map[string]any{"event": "pong", "id": q["id"]})
			d.Write(append(b, '\n'))
		}
	}()
	defer d.Close()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := c.Call(ctx, map[string]any{"cmd": "ping"}, false); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}
func TestPhysicalBindingAndNumberFallback(t *testing.T) {
	db, e := store.Open(filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := New(config.Module{ID: "one", Port: "COM1", Number: "+12025550101"}, db, log)
	s := domain.Envelope{Protocol: 2, DeviceID: "device1", ICCID: "sim1", Core: "V2044"}
	if e = m.acceptStatus(s); e != nil {
		t.Fatal(e)
	}
	if m.Snapshot().Receiver != "+12025550101" {
		t.Fatal(m.Snapshot())
	}
	s.Number = "+12025550100"
	m.acceptStatus(s)
	if m.Snapshot().Receiver != s.Number {
		t.Fatal(m.Snapshot())
	}
	s.DeviceID = "other"
	if m.acceptStatus(s) == nil {
		t.Fatal("rebound silently")
	}
	s.DeviceID = "device1"
	other := New(config.Module{ID: "two", Port: "COM2"}, db, log)
	if other.acceptStatus(s) == nil {
		t.Fatal("duplicate physical device allowed")
	}
	s.Protocol = 1
	if m.acceptStatus(s) == nil {
		t.Fatal("old protocol allowed")
	}
}

func TestModuleRunPersistentIngestAckAndSend(t *testing.T) {
	db, e := store.Open(filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	m := New(config.Module{ID: "a", Port: "fake"}, db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	host, device := net.Pipe()
	m.Open = func(string) (io.ReadWriteCloser, error) { return host, nil }
	acks := make(chan bool, 2)
	seen := make(chan struct{}, 2)
	m.Handle = func(ctx context.Context, id domain.Identity, e domain.Envelope) error {
		if id.Module != "a" || e.Text != "hello" {
			t.Error(id, e)
		}
		seen <- struct{}{}
		return nil
	}
	go func() {
		defer device.Close()
		scan := bufio.NewScanner(device)
		sent := false
		for scan.Scan() {
			var q map[string]any
			json.Unmarshal(scan.Bytes(), &q)
			reply := map[string]any{"id": q["id"]}
			switch q["cmd"] {
			case "status":
				reply["event"] = "status"
				reply["protocol"] = 3
				reply["capabilities"] = map[string]bool{"guarded_send": true}
				reply["device_id"] = "dev"
				reply["iccid"] = "sim"
				reply["network"] = 1
				reply["sms_ready_event_seen"] = true
				reply["core"] = "V2044"
			case "inbox":
				reply["event"] = "inbox"
				if !sent {
					reply["messages"] = []domain.Envelope{{Event: "sms_received", BootID: "b", MessageID: 1, Text: "hello", ICCID: "sim"}}
					sent = true
				}
			case "ack":
				acks <- true
				reply["event"] = "ack"
			case "sms_send":
				if q["expected_iccid"] != "sim" {
					t.Error("missing physical SIM guard")
				}
				reply["event"] = "sms_result"
				reply["submitted"] = true
			}
			b, _ := json.Marshal(reply)
			if _, e := device.Write(append(b, '\n')); e != nil {
				return
			}
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	select {
	case <-seen:
	case <-time.After(2 * time.Second):
		t.Fatal("no incoming event")
	}
	select {
	case <-acks:
	case <-time.After(time.Second):
		t.Fatal("no durable ingest ack")
	}
	callCtx, end := context.WithTimeout(ctx, time.Second)
	defer end()
	out, e := m.Send(callCtx, "test-send", "50000", "101", m.Snapshot().Identity)
	if e != nil || out.Submitted == nil || !*out.Submitted {
		t.Fatal(out, e)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("module failed to shut down")
	}
}

func TestFreshStatusRejectsSIMSwapBeforeSMSWrite(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := New(config.Module{ID: "one"}, db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	status := domain.Envelope{Protocol: 3, DeviceID: "physical", ICCID: "old-sim", Number: "+12025550100", SMSReady: true, Capabilities: map[string]bool{"guarded_send": true}}
	if err = m.acceptStatus(status); err != nil {
		t.Fatal(err)
	}
	expected := m.Snapshot().Identity
	host, device := net.Pipe()
	c := NewConnection(host)
	m.conn = c
	defer c.Close()
	go c.ReadLoop()
	commands := make(chan string, 4)
	go func() {
		defer device.Close()
		scan := bufio.NewScanner(device)
		for scan.Scan() {
			var q map[string]any
			json.Unmarshal(scan.Bytes(), &q)
			commands <- q["cmd"].(string)
			b, _ := json.Marshal(map[string]any{"id": q["id"], "event": "status", "iccid": "replacement-sim"})
			device.Write(append(b, '\n'))
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = m.Send(ctx, "request", "50000", "test", expected); !errors.Is(err, ErrSIMChanged) {
		t.Fatal(err)
	}
	if len(commands) != 1 || <-commands != "status" {
		t.Fatal("SMS written despite changed SIM")
	}
}
