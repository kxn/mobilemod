package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kxn/mobilemod/internal/domain"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// Exercise the actual official SDK: HTTP bootstrap, binary protobuf websocket
// frame, event dispatcher, our normalization/handler and success/failure ACKs.
func TestOfficialWebSocketDispatchAndPersistenceFailureACK(t *testing.T) {
	connected := make(chan *websocket.Conn, 1)
	responses := make(chan int, 4)
	upgrader := websocket.Upgrader{}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		connected <- conn
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var f larkws.Frame
			if f.Unmarshal(raw) != nil || f.Method != int32(larkws.FrameTypeData) {
				continue
			}
			var result larkws.Response
			if json.Unmarshal(f.Payload, &result) == nil {
				responses <- result.StatusCode
			}
		}
	}))
	defer gateway.Close()
	bootstrap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != larkws.GenEndpointUri {
			t.Error(r.URL.Path)
		}
		json.NewEncoder(w).Encode(larkws.EndpointResp{Code: 0, Data: &larkws.Endpoint{Url: "ws" + strings.TrimPrefix(gateway.URL, "http") + "/?device_id=test&service_id=1", ClientConfig: &larkws.ClientConfig{PingInterval: 3600, ReconnectInterval: 1}}})
	}))
	defer bootstrap.Close()
	var calls atomic.Int32
	l := &Listener{AppID: "test-app", Secret: "test-secret", domain: bootstrap.URL, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	l.Handle = func(_ context.Context, in domain.Incoming) error {
		if in.Sender != "owner" || in.Text != "/status" || in.ChatID != "chat" || in.MessageID != "message" {
			t.Errorf("wrong incoming: %+v", in)
		}
		if calls.Add(1) == 1 {
			return errors.New("disk unavailable")
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()
	var conn *websocket.Conn
	select {
	case conn = <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("SDK did not connect")
	}
	defer conn.Close()
	payload := []byte(fmt.Sprintf(`{"schema":"2.0","header":{"event_id":"event","event_type":"im.message.receive_v1","create_time":"%d","app_id":"test-app"},"event":{"sender":{"sender_id":{"open_id":"owner"},"sender_type":"user"},"message":{"message_id":"message","chat_id":"chat","chat_type":"p2p","message_type":"text","create_time":"%d","content":"{\"text\":\"/status\"}"}}}`, time.Now().UnixMilli(), time.Now().UnixMilli()))
	for i, expected := range []int{500, 200} {
		frame := larkws.Frame{SeqID: uint64(i + 1), Method: int32(larkws.FrameTypeData), Service: 1, Headers: []larkws.Header{{Key: larkws.HeaderType, Value: "event"}, {Key: larkws.HeaderSum, Value: "1"}, {Key: larkws.HeaderSeq, Value: "0"}, {Key: larkws.HeaderMessageID, Value: fmt.Sprint(i)}}, Payload: payload}
		raw, err := frame.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if err = conn.WriteMessage(websocket.BinaryMessage, raw); err != nil {
			t.Fatal(err)
		}
		select {
		case code := <-responses:
			if code != expected {
				t.Fatalf("ACK=%d want %d", code, expected)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("missing SDK ACK")
		}
	}
	if calls.Load() != 2 || !l.IsReady() {
		t.Fatal("handler/readiness", calls.Load(), l.IsReady())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SDK shutdown blocked")
	}
	if l.IsReady() {
		t.Fatal("stopped listener still ready")
	}
}

func TestMissingAPIResponseCodeCannotDiscardNotification(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) }))
	defer s.Close()
	c := New("app", "secret")
	c.BaseURL = s.URL
	c.token = "cached"
	c.expires = time.Now().Add(time.Hour)
	if err := c.SendText(context.Background(), domain.Recipient{Type: "open_id", ID: "owner"}, "message", "id"); err == nil {
		t.Fatal("malformed response was treated as delivered")
	}
}
