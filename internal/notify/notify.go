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
type TrackedSender interface {
	SendTracked(context.Context, domain.Recipient, string, string) (domain.Delivery, error)
}

func Format(m domain.Message) string {
	const friendlyTime = "2006-01-02 15:04:05"
	if m.Kind == "call" {
		text := fmt.Sprintf("【来电通知】\n接收号码：%s\n来电号码：%s\n时间：%s", m.Identity.Receiver, m.From, m.ReceivedAt.Format(friendlyTime))
		if m.Text != "" {
			text += "\n\n" + m.Text
		}
		if m.TimeNote != "" {
			text += "\n" + m.TimeNote
		}
		return text
	}
	state := map[string]string{"complete": "完整", "partial": "缺片，正文不完整", "ambiguous": "分片冲突，不能确认完整性", "invalid": "无效分片元数据", "unsupported": "当前模块不支持下载"}[m.State]
	if state == "" {
		state = m.State
	}
	header := "【收到短信】"
	if m.Kind == "mms" {
		header = "【彩信通知】"
	}
	lines := []string{
		header,
		"接收号码：" + m.Identity.Receiver,
		"来源号码：" + m.From,
		"接收时间：" + m.ReceivedAt.Format(friendlyTime),
	}
	if m.State != "complete" && state != "" {
		if m.Kind == "sms" && m.Expected > 0 {
			state = fmt.Sprintf("%s（%d/%d片）", state, m.Parts, m.Expected)
		}
		lines = append(lines, "状态："+state)
	}
	lines = append(lines, "", m.Text)
	return strings.Join(lines, "\n")
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
func EnqueueText(tx *store.Tx, id string, to domain.Recipient, text string, now time.Time, sourceID ...string) error {
	return enqueueText(tx, id, to, text, now, false, sourceID...)
}

func enqueueText(tx *store.Tx, id string, to domain.Recipient, text string, now time.Time, forward bool, sourceID ...string) error {
	parts := Split(text, 2500)
	for i, p := range parts {
		key := domain.Key(id, to.Type, to.ID, fmt.Sprint(i))
		if tx.Has("outbox", key) {
			continue
		}
		if len(parts) > 1 {
			p = fmt.Sprintf("[转发 %d/%d]\n", i+1, len(parts)) + p
		}
		source := ""
		if len(sourceID) > 0 {
			source = sourceID[0]
		}
		var reply *domain.ReplyTarget
		if source != "" {
			var m domain.Message
			if err := tx.Get("messages", source, &m); err != nil {
				return err
			}
			reply = &domain.ReplyTarget{SourceID: m.ID, Receiver: m.Identity.Receiver, From: m.From, SIM: m.Identity.SIM, Module: m.Identity.Module}
		}
		if err := tx.Put("outbox", key, domain.Outbox{ID: key, Recipient: to, Text: p, CreatedAt: now.Add(time.Duration(i)), NextAttempt: now, Forward: forward, SourceID: source, Reply: reply}); err != nil {
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
		source := ""
		if m.Kind == "sms" {
			source = m.ID
		}
		if err := enqueueText(tx, "sms:"+m.ID, r, Format(m), m.ReceivedAt, true, source); err != nil {
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

// truncate bounds persisted diagnostics so a chatty transport error cannot
// grow individual outbox records without limit.
func truncate(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
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
		var delivery domain.Delivery
		var err error
		if tracked, ok := w.Sender.(TrackedSender); ok {
			delivery, err = tracked.SendTracked(ctx, item.Recipient, item.Text, item.ID)
		} else {
			err = w.Sender.SendText(ctx, item.Recipient, item.Text, item.ID)
		}
		if err == nil && item.SourceID != "" {
			if _, ok := w.Sender.(TrackedSender); ok && (delivery.MessageID == "" || delivery.ChatID == "") {
				err = fmt.Errorf("missing Feishu delivery identity")
			}
		}
		item.Attempts++
		if err == nil {
			t := now
			item.SentAt = &t
			item.LastCode = ""
			item.LastError = ""
			item.ProviderID = delivery.MessageID
			item.ChatID = delivery.ChatID
		} else {
			item.LastCode = "delivery_failed"
			item.LastError = truncate(err.Error(), 500)
			t := now
			item.LastAttemptAt = &t
			item.NextAttempt = now.Add(Backoff(item.Attempts))
			if w.Log != nil {
				w.Log.Warn("Feishu delivery deferred", "notification", item.ID, "attempt", item.Attempts, "error", item.LastError)
			}
		}
		if e := w.Store.Update(func(tx *store.Tx) error {
			if item.SentAt != nil && item.SourceID != "" && delivery.MessageID != "" {
				if item.Reply == nil {
					return fmt.Errorf("missing reply correlation data")
				}
				link := *item.Reply
				link.MessageID = delivery.MessageID
				link.ChatID = delivery.ChatID
				link.CreatedAt = now
				if err := tx.Put("replies", delivery.MessageID, link); err != nil {
					return err
				}
			}
			return tx.Put("outbox", item.ID, item)
		}); e != nil {
			return e
		}
	}
	return nil
}
