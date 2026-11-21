package service

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/kxn/mobilemod/internal/domain"
	"github.com/kxn/mobilemod/internal/store"
)

// reconcilePendingRecipients repairs notifications queued for a recipient
// which is no longer configured, for example after replacing the Feishu app
// (open_id values are scoped to one app). An unambiguous single-recipient
// configuration is the only case which can be repaired automatically; with
// multiple recipients the intended destination is unknown, so those records
// are left untouched and surfaced through the delivery error in the UI.
func (r *Runtime) reconcilePendingRecipients(now time.Time) error {
	if len(r.Config.Feishu.Recipients) != 1 {
		return nil
	}
	target := r.Config.Feishu.Recipients[0]
	if target.Type == "" || target.ID == "" {
		return nil
	}
	type rebind struct {
		key  string
		item domain.Outbox
	}
	var changes []rebind
	if err := r.Store.View(func(tx *store.Tx) error {
		return tx.Scan("outbox", func(key string, b []byte) error {
			var item domain.Outbox
			if err := json.Unmarshal(b, &item); err != nil {
				return err
			}
			if item.SentAt != nil || item.Recipient == target {
				return nil
			}
			item.Recipient = target
			item.Attempts = 0
			item.NextAttempt = now
			item.LastCode = ""
			item.LastError = ""
			item.LastAttemptAt = nil
			changes = append(changes, rebind{key: key, item: item})
			return nil
		})
	}); err != nil {
		return err
	}
	if len(changes) == 0 {
		return nil
	}
	if err := r.Store.Update(func(tx *store.Tx) error {
		for _, change := range changes {
			if err := tx.Put("outbox", change.key, change.item); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	for _, change := range changes {
		log := r.Log
		if log == nil {
			log = slog.Default()
		}
		log.Warn("pending notification rebound to configured recipient",
			"notification", change.item.ID,
			"recipient_type", change.item.Recipient.Type)
	}
	return nil
}
