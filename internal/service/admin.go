package service

import (
	"encoding/json"
	"errors"
	"sort"

	"github.com/kxn/mobilemod/internal/admin"
	"github.com/kxn/mobilemod/internal/domain"
	"github.com/kxn/mobilemod/internal/modem"
	"github.com/kxn/mobilemod/internal/store"
)

func (r *Runtime) AdminStatus() (admin.Status, error) {
	status := admin.Status{Feishu: r.Listener.IsReady(), Modules: []modem.Snapshot{}}
	client := r.feishu.current()
	status.FeishuAppID, status.FeishuSecretSet = client.AppID, client.Secret != ""
	for _, m := range r.modules() {
		status.Modules = append(status.Modules, m.Snapshot())
	}
	sort.Slice(status.Modules, func(i, j int) bool { return status.Modules[i].ID < status.Modules[j].ID })
	err := r.Store.View(func(tx *store.Tx) error {
		return tx.Scan("outbox", func(_ string, b []byte) error {
			var item domain.Outbox
			if err := json.Unmarshal(b, &item); err != nil {
				return err
			}
			if item.SentAt != nil {
				return nil
			}
			status.PendingNotifications++
			if item.LastError == "" {
				return nil
			}
			status.FailedNotifications++
			// Prefer the most recent failure so the page explains the current
			// problem instead of an error that has already been superseded.
			if status.LastDeliveryErrorAt == nil || (item.LastAttemptAt != nil && item.LastAttemptAt.After(*status.LastDeliveryErrorAt)) {
				status.LastDeliveryError = item.LastError
				status.LastDeliveryErrorAt = item.LastAttemptAt
			}
			return nil
		})
	})
	return status, err
}
func (r *Runtime) SetModuleNumber(module, sim, number string) error {
	for _, m := range r.modules() {
		if m.Config.ID == module {
			return m.SetNumber(sim, number)
		}
	}
	return errors.New("module not found")
}
