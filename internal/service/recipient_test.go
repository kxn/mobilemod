package service

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/kxn/mobilemod/internal/config"
	"github.com/kxn/mobilemod/internal/domain"
	"github.com/kxn/mobilemod/internal/sms"
	"github.com/kxn/mobilemod/internal/store"
)

func TestSetRecipientPersistsLatestP2PSender(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r := &Runtime{Store: db, recipients: []domain.Recipient{{Type: "open_id", ID: "old"}}, Log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
	changed, err := r.setRecipient("ou_new")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected recipient change")
	}
	got := r.recipientsSnapshot()
	if len(got) != 1 || got[0].Type != "open_id" || got[0].ID != "ou_new" {
		t.Fatalf("unexpected recipients: %+v", got)
	}
	var saved domain.Recipient
	if err := db.View(func(tx *store.Tx) error {
		return tx.Get("settings", lastSenderSetting, &saved)
	}); err != nil {
		t.Fatal(err)
	}
	if saved.ID != "ou_new" {
		t.Fatalf("unexpected persisted recipient: %+v", saved)
	}
	changed, err = r.setRecipient("ou_new")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("unchanged recipient should not be rewritten")
	}
}

func TestHandleFeishuMessageBindsLatestP2PSender(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := config.Config{Feishu: config.Feishu{AllowedUsers: []string{"old"}}}
	r := &Runtime{Config: cfg, Store: db, recipients: []domain.Recipient{{Type: "open_id", ID: "old"}}}
	r.Engine = &Engine{Config: cfg, Store: db, Log: r.log(), Modules: map[string]Device{}}
	in := domain.Incoming{MessageID: "m1", ChatID: "oc_new", ChatType: "p2p", Sender: "ou_new", Text: "/help", CreatedAt: time.Now()}
	if err := r.handleFeishuMessage(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	got := r.recipientsSnapshot()
	if len(got) != 1 || got[0].ID != "ou_new" {
		t.Fatalf("unexpected recipients: %+v", got)
	}
	if err := db.View(func(tx *store.Tx) error {
		if !tx.Has("commands", "m1") {
			t.Fatal("p2p command was not accepted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestNewUsesLastP2PSender(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *store.Tx) error {
		return tx.Put("settings", lastSenderSetting, domain.Recipient{Type: "open_id", ID: "ou_saved"})
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Database: path, Feishu: config.Feishu{AppID: "app", AppSecret: "secret", AllowedUsers: []string{"owner"}, Recipients: []domain.Recipient{{Type: "open_id", ID: "owner"}}}, AssemblyTimeoutSeconds: 10, MaxParts: 32, SendLimitPerHour: 20, RetentionDays: 30}
	r, err := New(cfg, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Store.Close()
	got := r.recipientsSnapshot()
	if len(got) != 1 || got[0].ID != "ou_saved" {
		t.Fatalf("unexpected recipients: %+v", got)
	}
}

func TestAssemblerFollowsCurrentRecipient(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r := &Runtime{Store: db, recipients: []domain.Recipient{{Type: "open_id", ID: "old"}}}
	a := &sms.Assembler{Store: db, RecipientsProvider: r.recipientsSnapshot, Timeout: time.Minute, MaxParts: 32}
	identity := domain.Identity{Module: "m", Receiver: "+1", SIM: "s"}
	if err := a.Ingest(identity, domain.Envelope{Event: "sms_received", MessageID: 1, BootID: "boot", From: "95588", Text: "first"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.setRecipient("new"); err != nil {
		t.Fatal(err)
	}
	if err := a.Ingest(identity, domain.Envelope{Event: "sms_received", MessageID: 2, BootID: "boot", From: "95588", Text: "second"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	if err := db.View(func(tx *store.Tx) error {
		return tx.Scan("outbox", func(_ string, b []byte) error {
			var item domain.Outbox
			if err := json.Unmarshal(b, &item); err != nil {
				return err
			}
			seen[item.Recipient.ID] = true
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if !seen["old"] || !seen["new"] {
		t.Fatalf("assembler did not follow recipient change: %+v", seen)
	}
}

func TestNewFallsBackOnCorruptLastSender(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *store.Tx) error {
		return tx.Put("settings", lastSenderSetting, "not-a-recipient")
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Database: path, Feishu: config.Feishu{AppID: "app", AppSecret: "secret", Recipients: []domain.Recipient{{Type: "open_id", ID: "owner"}}}, AssemblyTimeoutSeconds: 10, MaxParts: 32, SendLimitPerHour: 20, RetentionDays: 30}
	r, err := New(cfg, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Store.Close()
	got := r.recipientsSnapshot()
	if len(got) != 1 || got[0].ID != "owner" {
		t.Fatalf("unexpected recipients: %+v", got)
	}
}
