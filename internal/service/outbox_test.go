package service

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kxn/mobilemod/internal/config"
	"github.com/kxn/mobilemod/internal/domain"
	"github.com/kxn/mobilemod/internal/store"
)

func TestReconcilePendingRecipientsSingleTarget(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	attempted := now.Add(-time.Minute)
	oldRecipient := domain.Recipient{Type: "open_id", ID: "ou_old"}
	target := domain.Recipient{Type: "open_id", ID: "ou_new"}
	sentAt := now.Add(-time.Hour)
	items := []domain.Outbox{
		{ID: "pending", Recipient: oldRecipient, Forward: true, Attempts: 7, NextAttempt: now.Add(time.Hour), LastCode: "delivery_failed", LastError: "open_id cross app", LastAttemptAt: &attempted},
		{ID: "already", Recipient: target, Attempts: 2, NextAttempt: now.Add(time.Hour)},
		{ID: "sent", Recipient: oldRecipient, Attempts: 3, SentAt: &sentAt},
		{ID: "reply", Recipient: oldRecipient, Attempts: 1, NextAttempt: now.Add(time.Hour)},
	}
	for _, item := range items {
		item := item
		if err := db.Update(func(tx *store.Tx) error { return tx.Put("outbox", item.ID, item) }); err != nil {
			t.Fatal(err)
		}
	}
	r := &Runtime{Store: db, Config: config.Config{Feishu: config.Feishu{Recipients: []domain.Recipient{target}}}}
	if err := r.reconcilePendingRecipients(now); err != nil {
		t.Fatal(err)
	}
	var pending domain.Outbox
	if err := db.View(func(tx *store.Tx) error { return tx.Get("outbox", "pending", &pending) }); err != nil {
		t.Fatal(err)
	}
	if pending.Recipient != target {
		t.Fatalf("pending recipient: %+v", pending.Recipient)
	}
	if pending.Attempts != 0 || pending.LastCode != "" || pending.LastError != "" || pending.LastAttemptAt != nil {
		t.Fatalf("pending retry state not reset: %+v", pending)
	}
	if !pending.NextAttempt.Equal(now) {
		t.Fatalf("pending next attempt: %v", pending.NextAttempt)
	}
	var already domain.Outbox
	if err := db.View(func(tx *store.Tx) error { return tx.Get("outbox", "already", &already) }); err != nil {
		t.Fatal(err)
	}
	if already.Recipient != target || already.Attempts != 2 {
		t.Fatalf("already-target item changed: %+v", already)
	}
	var sent domain.Outbox
	if err := db.View(func(tx *store.Tx) error { return tx.Get("outbox", "sent", &sent) }); err != nil {
		t.Fatal(err)
	}
	if sent.Recipient != oldRecipient || sent.Attempts != 3 {
		t.Fatalf("sent item changed: %+v", sent)
	}
	var reply domain.Outbox
	if err := db.View(func(tx *store.Tx) error { return tx.Get("outbox", "reply", &reply) }); err != nil {
		t.Fatal(err)
	}
	if reply.Recipient != oldRecipient || reply.Attempts != 1 {
		t.Fatalf("command reply must keep its chat destination: %+v", reply)
	}
}

func TestReconcilePendingRecipientsMultipleTargetsNoop(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	oldRecipient := domain.Recipient{Type: "open_id", ID: "ou_old"}
	item := domain.Outbox{ID: "pending", Recipient: oldRecipient, Attempts: 4, NextAttempt: now.Add(time.Hour), LastError: "open_id cross app"}
	if err := db.Update(func(tx *store.Tx) error { return tx.Put("outbox", item.ID, item) }); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{Store: db, Config: config.Config{Feishu: config.Feishu{Recipients: []domain.Recipient{
		{Type: "open_id", ID: "ou_a"},
		{Type: "open_id", ID: "ou_b"},
	}}}}
	if err := r.reconcilePendingRecipients(now); err != nil {
		t.Fatal(err)
	}
	var got domain.Outbox
	if err := db.View(func(tx *store.Tx) error { return tx.Get("outbox", "pending", &got) }); err != nil {
		t.Fatal(err)
	}
	if got.Recipient != oldRecipient || got.Attempts != 4 || got.LastError != "open_id cross app" {
		t.Fatalf("ambiguous configuration must not rewrite the pending item: %+v", got)
	}
}
