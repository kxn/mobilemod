package service

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kxn/mobilemod/internal/domain"
	"github.com/kxn/mobilemod/internal/feishu"
	"github.com/kxn/mobilemod/internal/store"
)

type feishuCredentials struct {
	AppID  string `json:"app_id"`
	Secret string `json:"app_secret"`
}

// managedFeishu swaps complete clients instead of mutating credentials which
// may be in use by a notification request. Its supervisor owns every listener.
type managedFeishu struct {
	mu       sync.RWMutex
	client   *feishu.Client
	listener EventListener
	changed  chan struct{}
	factory  func(*feishu.Client) EventListener
}

func (m *managedFeishu) current() *feishu.Client { m.mu.RLock(); defer m.mu.RUnlock(); return m.client }
func (m *managedFeishu) replace(client *feishu.Client) {
	m.mu.Lock()
	m.client, m.listener = client, nil
	m.mu.Unlock()
	select {
	case m.changed <- struct{}{}:
	default:
	}
}
func (m *managedFeishu) IsReady() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.listener != nil && m.listener.IsReady()
}
func (m *managedFeishu) SendTracked(ctx context.Context, to domain.Recipient, text, uuid string) (domain.Delivery, error) {
	return m.current().SendTracked(ctx, to, text, uuid)
}
func (m *managedFeishu) SendText(ctx context.Context, to domain.Recipient, text, uuid string) error {
	return m.current().SendText(ctx, to, text, uuid)
}
func (m *managedFeishu) LookupMessage(ctx context.Context, id string) (domain.PostedMessage, error) {
	return m.current().LookupMessage(ctx, id)
}
func (m *managedFeishu) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		// Drain earlier updates before installing the latest listener.
		select {
		case <-m.changed:
		default:
		}
		m.mu.Lock()
		if m.client.AppID == "" || m.client.Secret == "" {
			m.listener = nil
			m.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil
			case <-m.changed:
			}
			continue
		}
		listener := m.factory(m.client)
		m.listener = listener
		m.mu.Unlock()
		child, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- listener.Run(child) }()
		select {
		case <-ctx.Done():
			cancel()
			<-done
			return nil
		case <-m.changed:
			cancel()
			<-done
		case <-done:
			cancel()
			// Bad app/event configuration must not take down the Web UI or
			// receiving modules; retry while allowing credentials to be fixed.
			m.mu.Lock()
			if m.listener == listener {
				m.listener = nil
			}
			m.mu.Unlock()
			timer := time.NewTimer(30 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-m.changed:
				timer.Stop()
			case <-timer.C:
			}
		}
	}
	return nil
}

func (r *Runtime) SetFeishu(ctx context.Context, id, secret string) error {
	r.credentialMu.Lock()
	defer r.credentialMu.Unlock()
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 128 || strings.ContainsAny(id, " \r\n\t") || len(secret) > 256 {
		return errors.New("invalid credentials")
	}
	current := r.feishu.current()
	// An empty secret preserves the existing one only for the same app.
	if secret == "" && id == current.AppID {
		secret = current.Secret
	}
	if secret == "" || strings.TrimSpace(secret) != secret {
		return errors.New("secret required")
	}
	client := feishu.New(id, secret)
	// Keep the injected API transport in local protocol tests.
	client.BaseURL, client.HTTP = current.BaseURL, current.HTTP
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := client.ValidateCredentials(checkCtx); err != nil {
		return errors.New("Feishu rejected credentials")
	}
	credentials := feishuCredentials{AppID: id, Secret: secret}
	if err := r.Store.Update(func(tx *store.Tx) error { return tx.Put("settings", "feishu_credentials", credentials) }); err != nil {
		return err
	}
	r.feishu.replace(client)
	return nil
}

func loadFeishuCredentials(db *store.Store, original feishuCredentials) (feishuCredentials, error) {
	var saved feishuCredentials
	err := db.View(func(tx *store.Tx) error { return tx.Get("settings", "feishu_credentials", &saved) })
	if errors.Is(err, os.ErrNotExist) {
		return original, nil
	}
	if err != nil {
		return saved, err
	}
	if saved.AppID == "" || saved.Secret == "" {
		return saved, errors.New("invalid stored Feishu credentials")
	}
	return saved, nil
}
