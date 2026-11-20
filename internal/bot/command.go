package bot

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kxn/mobilemod/internal/config"
	"github.com/kxn/mobilemod/internal/phone"
	"github.com/kxn/mobilemod/internal/sms"
)

const Help = `短信机器人命令：
/modules                       模块和接收号码
/status [号码或模块ID]         注册与短信状态
/sms 发送号码 目标号码 正文     发送一条短短信
/forward 发送号码 query [类型]  查询呼叫转移
/forward 发送号码 set 类型 号码 设置呼叫转移
/forward 发送号码 cancel 类型   取消呼叫转移
类型：unconditional / busy / no_reply / unreachable；cancel 可用 all。
引用回复机器人转发的短信，可直接给原发送者回短信。
国内手机号可省略+86。正文保留连续空格、换行和末尾空格。
呼转取决于硬件/运营商适配器；不可用时会明确拒绝。短信提交成功不等于送达。`

type Command struct{ Kind, Module, From, To, Text, Action, Mode string }

func token(s string) (string, string) {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	for i, r := range s {
		if unicode.IsSpace(r) {
			return s[:i], strings.TrimLeftFunc(s[i:], unicode.IsSpace)
		}
	}
	return s, ""
}

// The single whitespace after the destination separates syntax from payload.
// Everything after it is SMS data, including further spaces and trailing CR/LF.
func destination(s string) (string, string) {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	for i, r := range s {
		if unicode.IsSpace(r) {
			return s[:i], s[i+utf8.RuneLen(r):]
		}
	}
	return s, ""
}
func Parse(text string) (Command, error) {
	var c Command
	text = strings.TrimLeftFunc(text, unicode.IsSpace)
	for strings.HasPrefix(text, "@_user_") {
		_, text = token(text)
	}
	name, rest := token(text)
	name = strings.TrimPrefix(strings.ToLower(name), "/")
	c.Kind = name
	switch name {
	case "help", "start":
		if rest != "" {
			return c, errors.New("help不接受参数")
		}
		c.Kind = "help"
	case "modules":
		if rest != "" {
			return c, errors.New("modules不接受参数")
		}
	case "status":
		c.Module, rest = token(rest)
		if rest != "" {
			return c, errors.New("status参数过多")
		}
	case "sms":
		c.From, rest = token(rest)
		c.To, c.Text = destination(rest)
		c.From = phone.Normalize(c.From)
		c.To = phone.Normalize(c.To)
		if c.From == "" || c.To == "" {
			return c, errors.New("格式：/sms 发送号码 目标号码 正文")
		}
		if err := sms.ValidateShort(c.Text); err != nil {
			return c, err
		}
	case "forward":
		c.From, rest = token(rest)
		c.From = phone.Normalize(c.From)
		c.Action, rest = token(rest)
		c.Mode, rest = token(rest)
		if c.Mode == "" && c.Action == "query" {
			c.Mode = "unconditional"
		}
		if c.Action == "set" {
			c.To, rest = token(rest)
			c.To = phone.Normalize(c.To)
			if !config.ValidNumber(c.To) {
				return c, errors.New("呼转目标号码无效")
			}
		}
		validMode := c.Mode == "unconditional" || c.Mode == "busy" || c.Mode == "no_reply" || c.Mode == "unreachable" || (c.Mode == "all" && c.Action == "cancel")
		if c.From == "" || rest != "" || !validMode || (c.Action != "set" && c.Action != "query" && c.Action != "cancel") {
			return c, errors.New("格式：/forward 发送号码 query|set|cancel 类型 [号码]")
		}
	default:
		return c, errors.New("未知命令；发送 /help 查看帮助")
	}
	return c, nil
}
