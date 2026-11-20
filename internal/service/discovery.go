package service

import (
	"os"

	"github.com/kxn/mobilemod/internal/config"
	"github.com/kxn/mobilemod/internal/domain"
	"github.com/kxn/mobilemod/internal/modem"
	"github.com/kxn/mobilemod/internal/store"
)

func (r *Runtime) findModule(deviceID string) (*modem.Module, error) {
	r.modulesMu.Lock()
	defer r.modulesMu.Unlock()
	var owner string
	err := r.Store.View(func(tx *store.Tx) error { return tx.Get("settings", "physical:"+deviceID, &owner) })
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, m := range r.Modules {
		if m.Config.ID == owner || m.Config.ExpectedDevice == deviceID {
			return m, nil
		}
	}
	if owner == "" {
		owner = "usb-" + domain.Key(deviceID)[:12]
	}
	return r.addModule(config.Module{ID: owner, ExpectedDevice: deviceID}), nil
}
