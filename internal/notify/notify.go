package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/kxn/mobilemod/internal/domain"
	"github.com/kxn/mobilemod/internal/store"
)

type Sender interface {
	SendText(context.Context, domain.Recipient, string, string) error
}

func Format(m domain.Message) string {
	state := map[string]string{"complete": "完整", "partial": "缺片，正文不完整", "ambiguous": "分片冲突，不能确认完整性", "invalid": "无效分片元数据", "unsupported": "当前模块不支持下载"}[m.State]
	if state == "" {
		state = m.State
	}
	header := "【收到短信】"
	if m.Kind == "mms" {
		header = "【彩信通知】"
	}
	return fmt.Sprintf("%s\n接收号码：%s\n模块：%s\n来源号码：%s\n接收时间：%s\n状态：%s（%d/%d片）\n消息ID：%s\n\n%s", header, m.Identity.Receiver, m.Identity.Module, m.From, m.ReceivedAt.Format(time.RFC3339), state, m.Parts, m.Expected, m.ID, m.Text)
}
func Split(text string, maxRunes int) []string {
	if maxRunes < 1 {
		maxRunes = 2500
	}
	r := []rune(strings.ToValidUTF8(text, "�"))
	if len(r) == 0 {
		return []string{""}
	}
	var parts []string
	for len(r) > 0 {
		n := min(len(r), maxRunes)
		parts = append(parts, string(r[:n]))
		r = r[n:]
	}
	return parts
}
func EnqueueText(tx *store.Tx, id string, to domain.Recipient, text string, now time.Time) error {
	parts := Split(text, 2500)
	for i, p := range parts {
		key := domain.Key(id, to.Type, to.ID, fmt.Sprint(i))
		if tx.Has("outbox", key) {
			continue
		}
		if len(parts) > 1 {
			p = fmt.Sprintf("[转发 %d/%d]\n", i+1, len(parts)) + p
		}
		if err := tx.Put("outbox", key, domain.Outbox{ID: key, Recipient: to, Text: p, CreatedAt: now.Add(time.Duration(i)), NextAttempt: now}); err != nil {
			return err
		}
	}
	return nil
}
func EnqueueMessage(tx *store.Tx, m domain.Message, to []domain.Recipient) error {
	if err := tx.Put("messages", m.ID, m); err != nil {
		return err
	}
	for _, r := range to {
		if err := EnqueueText(tx, "sms:"+m.ID, r, Format(m), m.ReceivedAt); err != nil {
			return err
		}
	}
	return nil
}

type Worker struct {
	Store  *store.Store
	Sender Sender
	Log    *slog.Logger
}

func Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	return min(time.Second*time.Duration(1<<min(attempt, 8)), 5*time.Minute)
}
func (w Worker) Tick(ctx context.Context, now time.Time) error {
	var items []domain.Outbox
	if err := w.Store.View(func(tx *store.Tx) error {
		return tx.Scan("outbox", func(_ string, b []byte) error {
			var v domain.Outbox
			if err := json.Unmarshal(b, &v); err != nil {
				return err
			}
			if v.SentAt == nil && !v.NextAttempt.After(now) {
				items = append(items, v)
			}
			return nil
		})
	}); err != nil {
		return err
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	for _, item := range items[:min(len(items), 20)] {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := w.Sender.SendText(ctx, item.Recipient, item.Text, item.ID)
		item.Attempts++
		if err == nil {
			t := now
			item.SentAt = &t
			item.LastCode = ""
		} else {
			item.LastCode = "delivery_failed"
			item.NextAttempt = now.Add(Backoff(item.Attempts))
			w.Log.Warn("Feishu delivery deferred", "notification", item.ID, "attempt", item.Attempts)
		}
		if e := w.Store.Update(func(tx *store.Tx) error { return tx.Put("outbox", item.ID, item) }); e != nil {
			return e
		}
	}
	return nil
}
