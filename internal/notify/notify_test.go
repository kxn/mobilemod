package notify

import (
	"context"
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
func TestMessageIncludesReceiverAndModule(t *testing.T) {
	text := Format(domain.Message{Identity: domain.Identity{Module: "dongle-2", Receiver: "+12025550101"}, From: "50000", State: "partial", Text: "hello", Parts: 1, Expected: 3})
	for _, s := range []string{"dongle-2", "+12025550101", "50000", "缺片", "hello"} {
		if !strings.Contains(text, s) {
			t.Fatal(text)
		}
	}
}
