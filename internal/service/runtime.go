package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kxn/mobilemod/internal/admin"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/kxn/mobilemod/internal/config"
	"github.com/kxn/mobilemod/internal/domain"
	"github.com/kxn/mobilemod/internal/feishu"
	"github.com/kxn/mobilemod/internal/modem"
	"github.com/kxn/mobilemod/internal/notify"
	"github.com/kxn/mobilemod/internal/sms"
	"github.com/kxn/mobilemod/internal/store"
)

type EventListener interface {
	Run(context.Context) error
	IsReady() bool
}
type Runtime struct {
	Config       config.Config
	Store        *store.Store
	Engine       *Engine
	Modules      []*modem.Module
	Listener     EventListener
	Client       *feishu.Client
	Log          *slog.Logger
	assembler    *sms.Assembler
	feishu       *managedFeishu
	credentialMu sync.Mutex
	modulesMu    sync.RWMutex
	recipientsMu sync.RWMutex
	recipients   []domain.Recipient
	Discovery    *modem.Discovery
}

func New(c config.Config, log *slog.Logger) (*Runtime, error) {
	db, err := store.Open(c.Database)
	if err != nil {
		return nil, err
	}
	credentials, err := loadFeishuCredentials(db, feishuCredentials{AppID: c.Feishu.AppID, Secret: c.Feishu.AppSecret})
	if err != nil {
		db.Close()
		return nil, err
	}
	recipients := append([]domain.Recipient(nil), c.Feishu.Recipients...)
	saved, err := loadLastRecipient(db)
	if err != nil {
		log.Warn("failed to load last Feishu sender; using configured recipients", "error", err)
	}
	if saved.Type == "open_id" && saved.ID != "" {
		recipients = []domain.Recipient{saved}
	}
	r := &Runtime{Config: c, Store: db, Log: log, Client: feishu.New(credentials.AppID, credentials.Secret), recipients: recipients}
	r.Engine = &Engine{Config: c, Store: db, Log: log, Modules: map[string]Device{}}
	r.feishu = &managedFeishu{client: r.Client, changed: make(chan struct{}, 1)}
	r.feishu.factory = func(client *feishu.Client) EventListener {
		return &feishu.Listener{AppID: client.AppID, Secret: client.Secret, Log: log, Handle: r.handleFeishuMessage}
	}
	r.assembler = &sms.Assembler{Store: db, Recipients: c.Feishu.Recipients, RecipientsProvider: r.recipientsSnapshot, Timeout: time.Duration(c.AssemblyTimeoutSeconds) * time.Second, MaxParts: c.MaxParts}
	for _, cfg := range c.Modules {
		r.addModule(cfg)
	}
	r.Listener = r.feishu
	return r, nil
}
func (r *Runtime) startModules(ctx context.Context, wg *sync.WaitGroup) {
	if r.Config.AutoDiscover {
		discovery := r.Discovery
		if discovery == nil {
			discovery = &modem.Discovery{Log: r.Log, Find: r.findModule}
		}
		wg.Add(1)
		go func() { defer wg.Done(); discovery.Run(ctx) }()
		return
	}
	for _, m := range r.modules() {
		wg.Add(1)
		go func() { defer wg.Done(); m.Run(ctx) }()
	}
}

func (r *Runtime) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := r.Engine.Recover(); err != nil {
		return err
	}
	if err := r.reconcilePendingRecipients(time.Now()); err != nil {
		return err
	}
	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	var server *http.Server
	if r.Config.HealthListen != "" {
		listener, err := net.Listen("tcp", r.Config.HealthListen)
		if err != nil {
			return err
		}
		mux := http.NewServeMux()
		web := admin.New(r)
		mux.Handle("/", web)
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, req *http.Request) {
			if req.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"healthy":true}`))
		})
		mux.HandleFunc("/readyz", func(w http.ResponseWriter, req *http.Request) {
			online := 0
			for _, m := range r.modules() {
				if m.Snapshot().Online && m.Snapshot().Ready {
					online++
				}
			}
			ready := r.Listener.IsReady() && online > 0
			w.Header().Set("Content-Type", "application/json")
			if !ready {
				w.WriteHeader(503)
			}
			json.NewEncoder(w).Encode(map[string]any{"ready": ready, "feishu": r.Listener.IsReady(), "modules_online": online, "modules_configured": len(r.modules())})
		})
		server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 30 * time.Second}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := server.Serve(listener); e != nil && !errors.Is(e, http.ErrServerClosed) {
				select {
				case errCh <- errors.New("health listener stopped"):
				default:
				}
			}
		}()
	}
	r.startModules(ctx, &wg)
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := r.Listener.Run(ctx); err != nil {
			select {
			case errCh <- err:
			default:
			}
		}
	}()
	for i := 0; i < min(len(r.modules())+2, 16); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(200 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if err := r.Engine.Tick(ctx); err != nil {
						r.Log.Error("command worker persistence failure")
					}
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		worker := notify.Worker{Store: r.Store, Sender: r.feishu, Log: r.Log}
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				if err := worker.Tick(ctx, now); err != nil && ctx.Err() == nil {
					r.Log.Error("notification worker persistence failure")
				}
				if err := r.assembler.Expire(now); err != nil {
					r.Log.Error("assembly expiration persistence failure")
				}
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				if err := r.Engine.Cleanup(now); err != nil {
					r.Log.Error("retention cleanup failed")
				}
			}
		}
	}()
	r.Log.Info("gateway started", "modules", len(r.modules()))
	var result error
	select {
	case <-ctx.Done():
	case result = <-errCh:
	}
	cancel()
	if server != nil {
		shutdownCtx, end := context.WithTimeout(context.Background(), 5*time.Second)
		if err := server.Shutdown(shutdownCtx); err != nil {
			// Shutdown can time out on a TCP connection which has not sent
			// headers yet. Close it as well before releasing runtime resources.
			server.Close()
		}
		end()
	}
	wg.Wait()
	return result
}
func (r *Runtime) LocalSend(ctx context.Context, module, to, text, id string) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	r.startModules(ctx, &wg)
	defer func() { cancel(); wg.Wait() }()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for {
		m, ok := r.Engine.devices()[module]
		if !ok && !r.Config.AutoDiscover {
			return "", errors.New("unknown module")
		}
		if ok && m.Snapshot().Online && m.Snapshot().Ready {
			return r.Engine.LocalSend(ctx, module, to, text, id)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			return "", errors.New("module not ready after 15 seconds")
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// addModule is called while constructing the runtime or holding modulesMu.
func (r *Runtime) addModule(cfg config.Module) *modem.Module {
	m := modem.New(cfg, r.Store, r.Log)
	m.Notice = func(code string, count int) error {
		return r.Store.Update(func(tx *store.Tx) error {
			key := domain.Key("module_notice", cfg.ID, code, fmt.Sprint(count))
			if tx.Has("settings", key) {
				return nil
			}
			text := fmt.Sprintf("【模块状态告警】\n模块：%s\n类型：%s\n计数/消息编号：%d\n模块报告队列满或持久化失败，请检查设备连接和存储。已在电脑落盘的短信保持保留。", cfg.ID, code, count)
			for _, to := range r.recipientsSnapshot() {
				if err := notify.EnqueueText(tx, key, to, text, time.Now()); err != nil {
					return err
				}
			}
			return tx.Put("settings", key, true)
		})
	}
	m.Handle = func(ctx context.Context, id domain.Identity, e domain.Envelope) error {
		return r.assembler.Ingest(id, e, time.Now())
	}
	r.Modules = append(r.Modules, m)
	r.Engine.modulesMu.Lock()
	r.Engine.Modules[cfg.ID] = m
	r.Engine.modulesMu.Unlock()
	return m
}
func (r *Runtime) modules() []*modem.Module {
	r.modulesMu.RLock()
	defer r.modulesMu.RUnlock()
	return append([]*modem.Module(nil), r.Modules...)
}
