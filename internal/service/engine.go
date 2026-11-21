package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/kxn/mobilemod/internal/bot"
	"github.com/kxn/mobilemod/internal/config"
	"github.com/kxn/mobilemod/internal/domain"
	"github.com/kxn/mobilemod/internal/forwarding"
	"github.com/kxn/mobilemod/internal/modem"
	"github.com/kxn/mobilemod/internal/notify"
	"github.com/kxn/mobilemod/internal/phone"
	"github.com/kxn/mobilemod/internal/sms"
	"github.com/kxn/mobilemod/internal/store"
)

type Device interface {
	Snapshot() modem.Snapshot
	Send(context.Context, string, string, string, domain.Identity) (domain.Envelope, error)
}
type Engine struct {
	Config    config.Config
	Store     *store.Store
	Modules   map[string]Device
	Log       *slog.Logger
	modulesMu sync.RWMutex
}

func (e *Engine) log() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}

func contains(values []string, s string) bool {
	for _, v := range values {
		if v == s {
			return true
		}
	}
	return false
}
func (e *Engine) Accept(_ context.Context, in domain.Incoming) error {
	if in.ChatType == "p2p" {
		// The bot is private to its owner, so a p2p sender is accepted even
		// when the configuration still names a previous app's open_id. The
		// runtime records the latest p2p sender as the forwarding recipient.
		if in.Sender == "" {
			e.log().Info("Feishu message ignored: p2p message without sender", "chat_id", in.ChatID)
			return nil
		}
	} else {
		if !contains(e.Config.Feishu.AllowedUsers, in.Sender) {
			// Sender and chat IDs are required to repair the allow-list after an
			// app change; message text is deliberately not logged.
			e.log().Info("Feishu message ignored: sender not in allowed_users", "sender", in.Sender, "chat_id", in.ChatID, "chat_type", in.ChatType)
			return nil
		}
		if in.ChatType != "group" || !contains(e.Config.Feishu.AllowedChats, in.ChatID) {
			e.log().Info("Feishu message ignored: chat not in allowed_chats", "sender", in.Sender, "chat_id", in.ChatID, "chat_type", in.ChatType)
			return nil
		}
	}
	if in.MessageID == "" || in.ChatID == "" {
		e.log().Debug("Feishu message ignored: missing message or chat ID", "sender", in.Sender)
		return nil
	}
	now := time.Now()
	age := now.Sub(in.CreatedAt)
	if age > 5*time.Minute || age < -time.Minute {
		e.log().Debug("Feishu message ignored: outside processing window", "message_id", in.MessageID, "age", age)
		return nil
	}
	return e.Store.Update(func(tx *store.Tx) error {
		if tx.Has("commands", in.MessageID) {
			return nil
		}
		return tx.Put("commands", in.MessageID, domain.CommandJob{Incoming: in, State: "pending", CreatedAt: now})
	})
}
func (e *Engine) reply(job domain.CommandJob, text string) error {
	return e.Store.Update(func(tx *store.Tx) error {
		if err := notify.EnqueueText(tx, "reply:"+job.Incoming.MessageID, domain.Recipient{Type: "chat_id", ID: job.Incoming.ChatID}, text, time.Now()); err != nil {
			return err
		}
		job.State = "done"
		return tx.Put("commands", job.Incoming.MessageID, job)
	})
}
func (e *Engine) Recover() error {
	return e.Store.Update(func(tx *store.Tx) error {
		var jobs []domain.CommandJob
		if err := tx.Scan("commands", func(_ string, b []byte) error {
			var j domain.CommandJob
			if err := json.Unmarshal(b, &j); err != nil {
				return err
			}
			if j.State == "running" {
				jobs = append(jobs, j)
			}
			return nil
		}); err != nil {
			return err
		}
		for _, j := range jobs {
			j.State = "done"
			if err := tx.Put("commands", j.Incoming.MessageID, j); err != nil {
				return err
			}
			if err := notify.EnqueueText(tx, "reply:"+j.Incoming.MessageID, domain.Recipient{Type: "chat_id", ID: j.Incoming.ChatID}, "上一轮命令处理被中断；为避免重复扣费，没有自动重发短信或重做呼转。请检查设备和消息记录。", time.Now()); err != nil {
				return err
			}
		}
		return nil
	})
}
func (e *Engine) claim() (domain.CommandJob, bool, error) {
	var chosen domain.CommandJob
	found := false
	err := e.Store.Update(func(tx *store.Tx) error {
		if err := tx.Scan("commands", func(_ string, b []byte) error {
			var j domain.CommandJob
			if err := json.Unmarshal(b, &j); err != nil {
				return err
			}
			if j.State == "pending" && (!found || j.CreatedAt.Before(chosen.CreatedAt)) {
				chosen = j
				found = true
			}
			return nil
		}); err != nil {
			return err
		}
		if found {
			chosen.State = "running"
			return tx.Put("commands", chosen.Incoming.MessageID, chosen)
		}
		return nil
	})
	return chosen, found, err
}
func (e *Engine) Tick(ctx context.Context) error {
	job, ok, err := e.claim()
	if err != nil || !ok {
		return err
	}
	if time.Since(job.CreatedAt) > 5*time.Minute {
		return e.reply(job, "命令排队超过5分钟，已过期，未执行。")
	}
	if job.Incoming.ParentID != "" {
		return e.handleSMSReply(ctx, job)
	}
	c, err := bot.Parse(job.Incoming.Text)
	if err != nil {
		return e.reply(job, err.Error())
	}
	var result string
	switch c.Kind {
	case "help":
		result = bot.Help
	case "modules", "status":
		var ids []string
		for id, d := range e.devices() {
			if c.Module == "" || c.Module == id || phone.Equal(c.Module, d.Snapshot().Receiver) {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		if len(ids) == 0 {
			result = "模块ID不存在"
		}
		for _, id := range ids {
			s := e.devices()[id].Snapshot()
			result += fmt.Sprintf("模块：%s\n接收号码：%s\n在线：%t；网络：%d；CSQ：%d；短信就绪：%t\n固件：%s；协议：%d\n彩信下载：不支持\n\n", id, s.Receiver, s.Online, s.Network, s.CSQ, s.Ready, s.Core, s.Protocol)
			if s.IncomingCalls {
				result += "来电通知：已观察到来电事件\n"
			} else if s.PassiveCalls {
				result += "来电通知：被动监听，本次开机未观察到来电事件\n"
			} else {
				result += "来电通知：未启用被动监听\n"
			}
		}
	case "sms":
		return e.reply(job, e.submitFrom(ctx, job.Incoming.MessageID, c.From, c.To, c.Text))
	case "forward":
		device, state, sourceErr := e.source(c.From)
		if sourceErr != nil {
			return e.reply(job, sourceErr.Error())
		}
		var cfg config.Forwarding
		for _, m := range e.Config.Modules {
			if m.ID == state.ID {
				cfg = m.Forwarding
			}
		}
		c.To, c.Text, err = forwarding.Build(cfg, c)
		if err != nil {
			return e.reply(job, err.Error())
		}
		result = e.submitExpected(ctx, device, job.Incoming.MessageID, state.ID, c.To, c.Text, state)
		result += "\n这是运营商业务短信提交结果；呼转实际状态必须以运营商确认回复为准。"
	}
	return e.reply(job, result)
}
func (e *Engine) submit(ctx context.Context, device Device, commandID, module, to, text string) string {
	return e.submitExpected(ctx, device, commandID, module, to, text, device.Snapshot())
}
func (e *Engine) submitExpected(ctx context.Context, device Device, commandID, module, to, text string, s modem.Snapshot) string {
	if err := sms.ValidateShort(text); err != nil {
		return err.Error()
	}
	if !config.ValidNumber(to) {
		return "目标号码无效"
	}
	to = phone.Normalize(to)
	allowed := len(e.Config.AllowedDestinations) == 0
	for _, n := range e.Config.AllowedDestinations {
		if phone.Equal(n, to) {
			allowed = true
		}
	}
	if !allowed {
		return "目标号码不在允许列表中"
	}
	if !s.Online {
		return "模块离线，未发送"
	}
	if !s.Ready {
		return "模块短信功能尚未就绪，未发送"
	}
	if !s.GuardedSend {
		return "模块固件不支持发送前SIM校验，请升级桥接程序至协议3，未发送"
	}
	now := time.Now()
	id := domain.Key("send", commandID)
	record := domain.SendRecord{ID: id, Module: module, To: to, Text: text, State: "unknown", CreatedAt: now, UpdatedAt: now, From: s.Receiver, SIM: s.Identity.SIM}
	err := e.Store.Update(func(tx *store.Tx) error {
		if tx.Has("sends", id) {
			return errors.New("该命令已有发送记录；为避免重复扣费，不会重发")
		}
		count := 0
		if err := tx.Scan("sends", func(_ string, b []byte) error {
			var r domain.SendRecord
			if err := json.Unmarshal(b, &r); err != nil {
				return err
			}
			if r.Module == module && r.CreatedAt.After(now.Add(-time.Hour)) {
				count++
			}
			return nil
		}); err != nil {
			return err
		}
		if count >= e.Config.SendLimitPerHour {
			return errors.New("此模块已达到每小时发送上限，未发送")
		}
		// The uncertain state is fsynced BEFORE the write to the physical modem.
		return tx.Put("sends", id, record)
	})
	if err != nil {
		return err.Error()
	}
	sendCtx, cancel := context.WithTimeout(ctx, 110*time.Second)
	defer cancel()
	out, err := device.Send(sendCtx, id, to, text, s.Identity)
	if errors.Is(err, modem.ErrSIMChanged) {
		record.State = "rejected"
		record.Error = "source SIM/number changed before send"
	} else if err != nil {
		record.State = "unknown"
		record.Error = "connection or timeout; not automatically retried"
	} else if out.Submitted != nil {
		if *out.Submitted {
			record.State = "submitted"
		} else {
			record.State = "rejected"
		}
	} else {
		record.State = "unknown"
	}
	record.UpdatedAt = time.Now()
	if err = e.Store.Update(func(tx *store.Tx) error { return tx.Put("sends", id, record) }); err != nil {
		return "发送记录保存失败；结果未知，请勿直接重发。请求ID：" + id
	}
	switch record.State {
	case "submitted":
		return fmt.Sprintf("短信已提交给短信中心。\n模块：%s\n发送号码：%s\n目标号码：%s\n请求ID：%s\n这不代表接收手机已收到。", module, s.Receiver, to, id)
	case "rejected":
		if record.Error != "" {
			return "SIM或发送号码已变化，未发送；请刷新模块状态。请求ID：" + id
		}
		return "短信提交失败，未自动重试。请求ID：" + id
	default:
		return "短信结果未知，未自动重试。请检查手机与记录，避免重复扣费。请求ID：" + id
	}
}

// LocalSend uses the same durable send ledger/rate limits as Feishu commands.
func (e *Engine) LocalSend(ctx context.Context, module, to, text string, id string) (string, error) {
	d, ok := e.devices()[module]
	if !ok {
		return "", errors.New("unknown module")
	}
	return e.submit(ctx, d, "local:"+id, module, to, text), nil
}

func (e *Engine) Cleanup(now time.Time) error {
	cutoff := now.Add(-time.Duration(e.Config.RetentionDays) * 24 * time.Hour)
	return e.Store.Update(func(tx *store.Tx) error {
		for _, bucket := range []string{"raw", "messages", "outbox", "commands", "sends", "groups", "replies"} {
			var keys []string
			if err := tx.Scan(bucket, func(k string, b []byte) error {
				var at time.Time
				safe := true
				switch bucket {
				case "replies":
					var v domain.ReplyTarget
					if err := json.Unmarshal(b, &v); err != nil {
						return err
					}
					at = v.CreatedAt
				case "raw":
					var v sms.Raw
					if err := json.Unmarshal(b, &v); err != nil {
						return err
					}
					at = v.At
				case "messages":
					var v domain.Message
					if err := json.Unmarshal(b, &v); err != nil {
						return err
					}
					at = v.ReceivedAt
				case "outbox":
					var v domain.Outbox
					if err := json.Unmarshal(b, &v); err != nil {
						return err
					}
					at = v.CreatedAt
					safe = v.SentAt != nil
				case "commands":
					var v domain.CommandJob
					if err := json.Unmarshal(b, &v); err != nil {
						return err
					}
					at = v.CreatedAt
					safe = v.State == "done"
				case "sends":
					var v domain.SendRecord
					if err := json.Unmarshal(b, &v); err != nil {
						return err
					}
					at = v.CreatedAt
				case "groups":
					var v sms.Group
					if err := json.Unmarshal(b, &v); err != nil {
						return err
					}
					at = v.Started
					safe = v.Closed
				}
				if safe && !at.IsZero() && at.Before(cutoff) {
					keys = append(keys, k)
				}
				return nil
			}); err != nil {
				return err
			}
			for _, k := range keys {
				if err := tx.Delete(bucket, k); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (e *Engine) devices() map[string]Device {
	e.modulesMu.RLock()
	defer e.modulesMu.RUnlock()
	result := make(map[string]Device, len(e.Modules))
	for id, m := range e.Modules {
		result[id] = m
	}
	return result
}
