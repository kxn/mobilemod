package modem

import (
	"errors"
	"os"
	"time"

	"github.com/kxn/mobilemod/internal/phone"
	"github.com/kxn/mobilemod/internal/store"
)

var ErrSIMChanged = errors.New("SIM changed or unavailable; refresh the page")

type NumberBinding struct {
	Number    string    `json:"number"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (m *Module) resolveNumber(s Snapshot) Snapshot {
	s.DetectedNumber = phone.Normalize(s.DetectedNumber)
	s.ManualNumber = ""
	if s.SIMKey != "" && s.SIMKey != "unknown" {
		err := m.Store.View(func(tx *store.Tx) error {
			var b NumberBinding
			err := tx.Get("number_bindings", s.SIMKey, &b)
			if os.IsNotExist(err) {
				return nil
			}
			if err == nil {
				s.ManualNumber = b.Number
			}
			return err
		})
		if err != nil {
			s.Receiver = m.Config.ID
			s.NumberSource = "unavailable"
			s.Identity.Receiver = s.Receiver
			return s
		}
	}
	s.Receiver = m.Config.ID
	s.NumberSource = "module_id"
	if n := phone.Normalize(s.ManualNumber); n != "" {
		s.Receiver = n
		s.NumberSource = "manual"
	}
	if s.DetectedNumber != "" {
		s.Receiver = s.DetectedNumber
		s.NumberSource = "sim"
	}
	s.Identity.Receiver = s.Receiver
	return s
}

// SetNumber stores a fallback for the currently observed SIM, never for whatever
// SIM happens to occupy this physical port on a subsequent boot.
func (m *Module) SetNumber(expectedSIM, number string) error {
	n := phone.Normalize(number)
	if number != "" && n == "" {
		return errors.New("invalid receiving number")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.status.Online || expectedSIM == "" || expectedSIM == "unknown" || m.status.SIMKey != expectedSIM {
		return ErrSIMChanged
	}
	return m.Store.Update(func(tx *store.Tx) error { return tx.Put("number_bindings", expectedSIM, NumberBinding{n, time.Now()}) })
}

func (m *Module) seedNumber(tx *store.Tx, sim string) error {
	if sim == "unknown" {
		return nil
	}
	key := "number_config_seeded:" + m.Config.ID
	if tx.Has("settings", key) {
		return nil
	}
	if !tx.Has("number_bindings", sim) {
		if err := tx.Put("number_bindings", sim, NumberBinding{phone.Normalize(m.Config.Number), time.Now()}); err != nil {
			return err
		}
	}
	return tx.Put("settings", key, true)
}
