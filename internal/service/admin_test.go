package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/kxn/mobilemod/internal/domain"
	"github.com/kxn/mobilemod/internal/feishu"
	"github.com/kxn/mobilemod/internal/store"
)

type readyListener struct{ ready bool }

func (l readyListener) Run(context.Context) error { return nil }
func (l readyListener) IsReady() bool             { return l.ready }

func TestAdminStatusSurfacesLatestDeliveryFailure(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	old := now.Add(-time.Hour)
	items := []domain.Outbox{
		{ID: "older", CreatedAt: old, NextAttempt: now, LastError: "first failure", LastAttemptAt: &old},
		{ID: "latest", CreatedAt: now, NextAttempt: now, LastError: "open_id cross app", LastAttemptAt: &now},
		{ID: "clean", CreatedAt: now, NextAttempt: now},
	}
	for _, item := range items {
		item := item
		if err := db.Update(func(tx *store.Tx) error { return tx.Put("outbox", item.ID, item) }); err != nil {
			t.Fatal(err)
		}
	}
	sent := now
	if err := db.Update(func(tx *store.Tx) error {
		return tx.Put("outbox", "sent", domain.Outbox{ID: "sent", CreatedAt: now, NextAttempt: now, SentAt: &sent})
	}); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{Store: db, Listener: readyListener{ready: true}, feishu: &managedFeishu{client: feishu.New("app", "secret")}}
	status, err := r.AdminStatus()
	if err != nil {
		t.Fatal(err)
	}
	if !status.Feishu {
		t.Fatal("listener state lost")
	}
	if status.PendingNotifications != 3 || status.FailedNotifications != 2 {
		t.Fatalf("counts: %+v", status)
	}
	if status.LastDeliveryError != "open_id cross app" || status.LastDeliveryErrorAt == nil || !status.LastDeliveryErrorAt.Equal(now) {
		t.Fatalf("latest error: %+v", status)
	}
}
