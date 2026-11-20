package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/kxn/mobilemod/internal/domain"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func Normalize(event *larkim.P2MessageReceiveV1) (domain.Incoming, bool) {
	if event == nil || event.Event == nil || event.Event.Message == nil || event.Event.Sender == nil || event.Event.Sender.SenderId == nil {
		return domain.Incoming{}, false
	}
	m, s := event.Event.Message, event.Event.Sender
	if str(s.SenderType) != "user" || str(m.MessageType) != "text" {
		return domain.Incoming{}, false
	}
	var body struct {
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(str(m.Content)), &body) != nil || len(body.Text) > 16384 {
		return domain.Incoming{}, false
	}
	ms, err := strconv.ParseInt(str(m.CreateTime), 10, 64)
	if err != nil {
		return domain.Incoming{}, false
	}
	id := ""
	if event.EventV2Base != nil && event.EventV2Base.Header != nil {
		id = event.EventV2Base.Header.EventID
	}
	in := domain.Incoming{EventID: id, MessageID: str(m.MessageId), ChatID: str(m.ChatId), ChatType: str(m.ChatType), Sender: str(s.SenderId.OpenId), Text: body.Text, CreatedAt: time.UnixMilli(ms), ParentID: str(m.ParentId)}
	return in, in.MessageID != "" && in.ChatID != "" && in.Sender != ""
}

type Listener struct {
	AppID, Secret string
	Log           *slog.Logger
	Ready         atomic.Bool
	Handle        func(context.Context, domain.Incoming) error
	// domain is injected by protocol tests; production always uses the SDK default.
	domain string
}

func (l *Listener) IsReady() bool { return l.Ready.Load() }

func (l *Listener) Run(ctx context.Context) error {
	defer l.Ready.Store(false)
	h := dispatcher.NewEventDispatcher("", "").OnP2MessageReceiveV1(func(ctx context.Context, e *larkim.P2MessageReceiveV1) error {
		in, ok := Normalize(e)
		if !ok {
			return nil
		}
		return l.Handle(ctx, in)
	})
	opts := []larkws.ClientOption{larkws.WithEventHandler(h), larkws.WithLogger(quietLogger{}), larkws.WithLogLevel(larkcore.LogLevelError), larkws.WithHttpClient(&http.Client{Timeout: 15 * time.Second}), larkws.WithOnReady(func() { l.Ready.Store(true); l.Log.Info("Feishu long connection ready") }), larkws.WithOnDisconnected(func() { l.Ready.Store(false) }), larkws.WithOnReconnecting(func() { l.Ready.Store(false); l.Log.Warn("Feishu reconnecting") }), larkws.WithOnReconnected(func() { l.Ready.Store(true); l.Log.Info("Feishu reconnected") })}
	if l.domain != "" {
		opts = append(opts, larkws.WithDomain(l.domain))
	}
	client := larkws.NewClient(l.AppID, l.Secret, opts...)
	defer client.Close()
	err := client.Start(ctx)
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return errors.New("Feishu long connection stopped; check app credentials/event subscription")
	}
	return nil
}

// Suppress SDK formatting of full websocket URLs, query credentials and events.
type quietLogger struct{}

func (quietLogger) Debug(context.Context, ...interface{}) {}
func (quietLogger) Info(context.Context, ...interface{})  {}
func (quietLogger) Warn(context.Context, ...interface{})  {}
func (quietLogger) Error(context.Context, ...interface{}) {}
