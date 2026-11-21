// Package forwarding translates explicit operator-verified SMS templates only.
// Air780EPM/106 does not expose CCFC/MMI, and this package never invents codes or
// reports a requested change as an already-applied carrier setting.
package forwarding

import (
	"errors"
	"strings"

	"github.com/kxn/mobilemod/internal/bot"
	"github.com/kxn/mobilemod/internal/config"
	"github.com/kxn/mobilemod/internal/sms"
)

var ErrUnsupported = errors.New("此模块不支持呼叫转移")

func Build(cfg config.Forwarding, c bot.Command) (to, text string, err error) {
	if cfg.Backend != "sms" {
		return "", "", ErrUnsupported
	}
	switch c.Action {
	case "query":
		text = cfg.Query
	case "set":
		text = cfg.Set[c.Mode]
	case "cancel":
		text = cfg.Cancel[c.Mode]
	default:
		return "", "", ErrUnsupported
	}
	if text == "" {
		return "", "", ErrUnsupported
	}
	if c.Action == "set" {
		if !config.ValidNumber(c.To) {
			return "", "", errors.New("invalid forwarding target")
		}
		if !strings.Contains(text, "{number}") {
			return "", "", errors.New("运营商设置模板缺少{number}")
		}
		text = strings.ReplaceAll(text, "{number}", c.To)
	}
	if strings.ContainsAny(text, "{}") {
		return "", "", errors.New("运营商模板有未解析占位符")
	}
	if err = sms.ValidateShort(text); err != nil {
		return "", "", err
	}
	if !config.ValidNumber(cfg.ServiceNumber) {
		return "", "", errors.New("invalid operator service number")
	}
	return cfg.ServiceNumber, text, nil
}
