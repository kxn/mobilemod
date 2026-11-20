package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/kxn/mobilemod/internal/config"
	"github.com/kxn/mobilemod/internal/domain"
	"github.com/kxn/mobilemod/internal/modem"
	"github.com/kxn/mobilemod/internal/store"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeDevice struct {
	mu         sync.Mutex
	sends      int
	unknown    bool
	state      modem.Snapshot
	texts      []string
	targets    []string
	identities []domain.Identity
}

func (f *fakeDevice) Snapshot() modem.Snapshot { f.mu.Lock(); defer f.mu.Unlock(); return f.state }
func (f *fakeDevice) Send(_ context.Context, _, to, text string, expected domain.Identity) (domain.Envelope, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state.Identity != expected {
		return domain.Envelope{}, errors.New("SIM changed")
	}
	f.sends++
	f.texts = append(f.texts, text)
	f.targets = append(f.targets, to)
	f.identities = append(f.identities, expected)
	if f.unknown {
		return domain.Envelope{}, errors.New("connection lost after write")
	}
	v := true
	return domain.Envelope{Submitted: &v}, nil
}
func engine(t *testing.T) (*Engine, *fakeDevice) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	d := &fakeDevice{state: testState("one", "+12025550100", "sim-one")}
	return &Engine{Store: db, Config: config.Config{Feishu: config.Feishu{AllowedUsers: []string{"owner"}}, Modules: []config.Module{{ID: "one"}}, SendLimitPerHour: 2, RetentionDays: 30}, Modules: map[string]Device{"one": d}}, d
}
func testState(id, number, sim string) modem.Snapshot {
	return modem.Snapshot{ID: id, Online: true, Ready: true, GuardedSend: true, Receiver: number, SIMKey: sim, Identity: domain.Identity{Module: id, Receiver: number, SIM: sim}}
}
func incoming(id, text string) domain.Incoming {
	return domain.Incoming{MessageID: id, ChatID: "chat", ChatType: "p2p", Sender: "owner", Text: text, CreatedAt: time.Now()}
}
func TestAuthorizationReplayAndDuplicateSafety(t *testing.T) {
	e, d := engine(t)
	ctx := context.Background()
	bad := incoming("bad", "/sms +12025550100 50000 101")
	bad.Sender = "attacker"
	e.Accept(ctx, bad)
	bad = incoming("old", "/sms +12025550100 50000 101")
	bad.CreatedAt = time.Now().Add(-time.Hour)
	e.Accept(ctx, bad)
	bad = incoming("group", "/sms +12025550100 50000 101")
	bad.ChatType = "group"
	e.Accept(ctx, bad)
	e.Tick(ctx)
	if d.sends != 0 {
		t.Fatal("unauthorized send")
	}
	in := incoming("cmd1", "/sms +12025550100 50000 101")
	e.Accept(ctx, in)
	e.Accept(ctx, in)
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	e.Accept(ctx, in)
	e.Tick(ctx)
	if d.sends != 1 {
		t.Fatal("duplicate send", d.sends)
	}
}
func TestUnknownOutcomeNotRetriedAndRateLimit(t *testing.T) {
	e, d := engine(t)
	ctx := context.Background()
	d.unknown = true
	for _, id := range []string{"a", "b", "c"} {
		e.Accept(ctx, incoming(id, "/sms +12025550100 50000 101"))
		e.Tick(ctx)
	}
	if d.sends != 2 {
		t.Fatal("rate limit", d.sends)
	}
	result := e.submit(ctx, d, "a", "one", "50000", "101")
	if d.sends != 2 || result == "" {
		t.Fatal("duplicate retried")
	}
	var records []domain.SendRecord
	e.Store.View(func(tx *store.Tx) error {
		return tx.Scan("sends", func(_ string, b []byte) error {
			var r domain.SendRecord
			json.Unmarshal(b, &r)
			records = append(records, r)
			return nil
		})
	})
	for _, r := range records {
		if r.State != "unknown" {
			t.Fatal(r)
		}
	}
}
func TestCrashRecoveryDoesNotReplayRunningCommands(t *testing.T) {
	e, d := engine(t)
	j := domain.CommandJob{Incoming: incoming("interrupted", "/sms +12025550100 50000 101"), State: "running", CreatedAt: time.Now()}
	e.Store.Update(func(tx *store.Tx) error { return tx.Put("commands", j.Incoming.MessageID, j) })
	if err := e.Recover(); err != nil {
		t.Fatal(err)
	}
	e.Tick(context.Background())
	if d.sends != 0 {
		t.Fatal("replayed interrupted command")
	}
	e.Store.View(func(tx *store.Tx) error {
		if tx.Count("outbox") != 1 {
			t.Fatal("no recovery notice")
		}
		return nil
	})
}
func TestReadCommandsAndUnavailableForwarding(t *testing.T) {
	e, d := engine(t)
	for i, cmd := range []string{"/help", "/modules", "/status one", "/forward +12025550100 query", "/sms absent 50000 101", "/status absent"} {
		e.Accept(context.Background(), incoming(string(rune('a'+i)), cmd))
		if err := e.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if d.sends != 0 {
		t.Fatal("read/unsupported command sent SMS")
	}
}
func TestParallelModuleIsolation(t *testing.T) {
	e, d := engine(t)
	second := &fakeDevice{state: testState("two", "+12025550101", "sim-two")}
	e.Modules["two"] = second
	ctx := context.Background()
	e.Accept(ctx, incoming("one", "/sms +12025550100 50000 101"))
	e.Accept(ctx, incoming("two", "/sms +12025550101 50000 101"))
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.Tick(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if d.sends != 1 || second.sends != 1 {
		t.Fatal(d.sends, second.sends)
	}
}
