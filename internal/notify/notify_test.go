package notify

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/kxn/mobilemod/internal/domain"
	"github.com/kxn/mobilemod/internal/store"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type sender struct {
	fail  bool
	calls int
	ids   []string
	texts []string
}

func (s *sender) SendText(_ context.Context, _ domain.Recipient, text string, id string) error {
	s.calls++
	s.ids = append(s.ids, id)
	s.texts = append(s.texts, text)
	if s.fail {
		return errors.New("network")
	}
	return nil
}

func TestLongNotificationChunkOrder(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	if err := db.Update(func(tx *store.Tx) error {
		return EnqueueText(tx, "ordered", domain.Recipient{Type: "open_id", ID: "owner"}, strings.Repeat("中", 6000), now)
	}); err != nil {
		t.Fatal(err)
	}
	s := &sender{}
	w := Worker{Store: db, Sender: s, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := w.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(s.texts) != 3 {
		t.Fatal(s.texts)
	}
	for i, prefix := range []string{"[转发 1/3]", "[转发 2/3]", "[转发 3/3]"} {
		if !strings.HasPrefix(s.texts[i], prefix) {
			t.Fatal(s.texts[i])
		}
	}
}
func TestDurableOutboxRetryAndUnicode(t *testing.T) {
	db, e := store.Open(filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	now := time.Now()
	text := strings.Repeat("中文🙂", 2000)
	parts := Split(text, 2500)
	if strings.Join(parts, "") != text || len(parts) != 3 {
		t.Fatal("unicode split")
	}
	db.Update(func(tx *store.Tx) error {
		return EnqueueText(tx, "msg", domain.Recipient{Type: "open_id", ID: "owner"}, "test", now)
	})
	fake := &sender{fail: true}
	w := Worker{Store: db, Sender: fake, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if e = w.Tick(context.Background(), now); e != nil {
		t.Fatal(e)
	}
	var failed domain.Outbox
	if e = db.View(func(tx *store.Tx) error {
		return tx.Scan("outbox", func(_ string, b []byte) error {
			return json.Unmarshal(b, &failed)
		})
	}); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(failed.LastError, "network") || failed.LastAttemptAt == nil {
		t.Fatalf("delivery error was not persisted: %+v", failed)
	}
	w.Tick(context.Background(), now)
	if fake.calls != 1 {
		t.Fatal("ignored backoff")
	}
	fake.fail = false
	w.Tick(context.Background(), now.Add(time.Minute))
	w.Tick(context.Background(), now.Add(2*time.Minute))
	if fake.calls != 2 || fake.ids[0] != fake.ids[1] {
		t.Fatal("uuid/retry", fake)
	}
	for i := 0; i < 100; i++ {
		if Backoff(i) > 5*time.Minute {
			t.Fatal("unbounded backoff")
		}
	}
}
func TestMessageIncludesReceiverAndSenderButNotModule(t *testing.T) {
	text := Format(domain.Message{Identity: domain.Identity{Module: "dongle-2", Receiver: "+12025550101"}, From: "50000", State: "partial", Text: "hello", Parts: 1, Expected: 3})
	for _, s := range []string{"+12025550101", "50000", "缺片", "hello"} {
		if !strings.Contains(text, s) {
			t.Fatal(text)
		}
	}
	for _, s := range []string{"dongle-2", "消息ID", "状态：完整"} {
		if strings.Contains(text, s) {
			t.Fatalf("unexpected %q in %q", s, text)
		}
	}
}

func TestCompleteMessageOmitsStatusAndUsesFriendlyTime(t *testing.T) {
	at := time.Date(2026, 11, 21, 9, 27, 41, 0, time.FixedZone("CST", 8*3600))
	text := Format(domain.Message{ID: "message-id", Identity: domain.Identity{Module: "dongle-2", Receiver: "+8610000000000"}, From: "95588", State: "complete", Text: "body", ReceivedAt: at, Parts: 3, Expected: 3})
	for _, s := range []string{"2026-11-21 09:27:41", "接收号码：+8610000000000", "body"} {
		if !strings.Contains(text, s) {
			t.Fatalf("missing %q in %q", s, text)
		}
	}
	for _, s := range []string{"状态", "片", "消息ID", "dongle-2", "T09:27:41", "+08:00"} {
		if strings.Contains(text, s) {
			t.Fatalf("unexpected %q in %q", s, text)
		}
	}
}

func TestCallNotification(t *testing.T) {
	at := time.Date(2026, 11, 21, 9, 27, 41, 0, time.FixedZone("CST", 8*3600))
	text := Format(domain.Message{Kind: "call", Identity: domain.Identity{Receiver: "+8610000000000"}, From: "+8610100000000", ReceivedAt: at})
	for _, want := range []string{"【来电通知】", "接收号码：+8610000000000", "来电号码：+8610100000000", "时间：2026-11-21 09:27:41"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
}

type trackedSender struct{ sender }

func (s *trackedSender) SendTracked(ctx context.Context, to domain.Recipient, text, id string) (domain.Delivery, error) {
	err := s.SendText(ctx, to, text, id)
	return domain.Delivery{MessageID: "provider-message", ChatID: "chat"}, err
}
func TestReplyMappingSurvivesSourceRetentionAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	m := domain.Message{ID: domain.Key("source"), Identity: domain.Identity{Module: "one", Receiver: "+12025550100", SIM: "sim"}, From: "+12025550101", Kind: "sms", State: "complete", Text: "test", ReceivedAt: now}
	if err = db.Update(func(tx *store.Tx) error {
		return EnqueueMessage(tx, m, []domain.Recipient{{Type: "open_id", ID: "owner"}})
	}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Update(func(tx *store.Tx) error { return tx.Delete("messages", m.ID) }); err != nil {
		t.Fatal(err)
	}
	s := &trackedSender{}
	w := Worker{Store: db, Sender: s, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err = w.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	var link domain.ReplyTarget
	if err = db.View(func(tx *store.Tx) error { return tx.Get("replies", "provider-message", &link) }); err != nil {
		t.Fatal(err)
	}
	if link.Receiver != m.Identity.Receiver || link.From != m.From || link.ChatID != "chat" {
		t.Fatal(link)
	}
}

func TestEnqueueMessageMarksForwarding(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	m := domain.Message{ID: "m1", Kind: "sms", Identity: domain.Identity{Receiver: "+1"}, From: "95588", Text: "hello", ReceivedAt: now}
	if err := db.Update(func(tx *store.Tx) error {
		return EnqueueMessage(tx, m, []domain.Recipient{{Type: "open_id", ID: "owner"}})
	}); err != nil {
		t.Fatal(err)
	}
	var items []domain.Outbox
	if err := db.View(func(tx *store.Tx) error {
		return tx.Scan("outbox", func(_ string, b []byte) error {
			var item domain.Outbox
			if err := json.Unmarshal(b, &item); err != nil {
				return err
			}
			items = append(items, item)
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !items[0].Forward || items[0].SourceID != "m1" {
		t.Fatalf("unexpected forwarding record: %+v", items)
	}
}
