package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/kxn/mobilemod/internal/domain"
	"github.com/kxn/mobilemod/internal/store"
)

const lastSenderSetting = "feishu_last_sender"

func (r *Runtime) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

func loadLastRecipient(db *store.Store) (domain.Recipient, error) {
	var saved domain.Recipient
	err := db.View(func(tx *store.Tx) error {
		if !tx.Has("settings", lastSenderSetting) {
			return nil
		}
		return tx.Get("settings", lastSenderSetting, &saved)
	})
	return saved, err
}

// recipientsSnapshot returns the current forwarding targets. A Runtime built
// without New falls back to the static configuration, which keeps tests and
// callers that only need the config working.
func (r *Runtime) recipientsSnapshot() []domain.Recipient {
	r.recipientsMu.RLock()
	current := append([]domain.Recipient(nil), r.recipients...)
	r.recipientsMu.RUnlock()
	if len(current) > 0 {
		return current
	}
	return append([]domain.Recipient(nil), r.Config.Feishu.Recipients...)
}

func (r *Runtime) setRecipient(sender string) (bool, error) {
	target := domain.Recipient{Type: "open_id", ID: sender}
	r.recipientsMu.Lock()
	defer r.recipientsMu.Unlock()
	if len(r.recipients) == 1 && r.recipients[0] == target {
		return false, nil
	}
	if err := r.Store.Update(func(tx *store.Tx) error {
		return tx.Put("settings", lastSenderSetting, target)
	}); err != nil {
		return false, err
	}
	r.recipients = []domain.Recipient{target}
	return true, nil
}

// handleFeishuMessage follows the latest p2p sender as the forwarding
// recipient. open_id values are scoped to a Feishu app, so after replacing the
// app the old value in the configuration cannot be used and there is no
// directory API available to resolve the new one; the event itself supplies it.
func (r *Runtime) handleFeishuMessage(ctx context.Context, in domain.Incoming) error {
	if in.ChatType == "p2p" && in.Sender != "" {
		age := time.Since(in.CreatedAt)
		if in.CreatedAt.IsZero() || (age <= 5*time.Minute && age >= -time.Minute) {
			changed, err := r.setRecipient(in.Sender)
			if err != nil {
				r.log().Error("failed to persist latest Feishu sender", "error", err)
			}
			if changed {
				r.log().Info("Feishu recipient updated from latest p2p sender", "sender", in.Sender)
				if err := r.reconcilePendingRecipients(time.Now()); err != nil {
					r.log().Error("failed to rebind pending notifications", "error", err)
				}
			}
		}
	}
	return r.Engine.Accept(ctx, in)
}
