// Package feishu uses the official Go SDK for long connections and a small,
// testable HTTP client for outbound text messages. Credentials never enter logs.
package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/kxn/mobilemod/internal/domain"
)

type APIError struct {
	HTTP int
	Code int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Feishu API rejected request (HTTP %d, code %d)", e.HTTP, e.Code)
}

type Client struct {
	AppID, Secret, BaseURL string
	HTTP                   *http.Client
	mu                     sync.Mutex
	token                  string
	expires                time.Time
}

func New(id, secret string) *Client {
	return &Client{AppID: id, Secret: secret, BaseURL: "https://open.feishu.cn", HTTP: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *Client) request(ctx context.Context, path, token string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := c.HTTP.Do(r)
	if err != nil {
		return fmt.Errorf("Feishu HTTP transport unavailable")
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	var meta struct {
		Code *int `json:"code"`
	}
	if err = json.Unmarshal(raw, &meta); err != nil {
		return fmt.Errorf("invalid Feishu API response")
	}
	if meta.Code == nil {
		return fmt.Errorf("missing Feishu API response code")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 || *meta.Code != 0 {
		return &APIError{res.StatusCode, *meta.Code}
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}
	var res struct {
		Token  string `json:"tenant_access_token"`
		Expire int    `json:"expire"`
	}
	err := c.request(ctx, "/open-apis/auth/v3/tenant_access_token/internal", "", map[string]string{"app_id": c.AppID, "app_secret": c.Secret}, &res)
	if err != nil {
		return "", err
	}
	if res.Token == "" || res.Expire <= 0 {
		return "", fmt.Errorf("empty Feishu token")
	}
	c.token = res.Token
	c.expires = time.Now().Add(time.Duration(max(1, res.Expire-120)) * time.Second)
	return c.token, nil
}
func (c *Client) SendText(ctx context.Context, to domain.Recipient, text, id string) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	content, _ := json.Marshal(map[string]string{"text": text})
	err = c.request(ctx, "/open-apis/im/v1/messages?receive_id_type="+url.QueryEscape(to.Type), token, map[string]string{"receive_id": to.ID, "msg_type": "text", "content": string(content), "uuid": id}, nil)
	if api, ok := err.(*APIError); ok && (api.Code == 99991663 || api.Code == 99991668) {
		c.mu.Lock()
		c.token = ""
		c.mu.Unlock()
	}
	return err
}
