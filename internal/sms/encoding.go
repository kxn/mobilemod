package sms

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// ValidateShort checks SMS code units, not UTF-8 bytes. Extended GSM7 characters
// occupy two septets. Non-BMP characters are refused for this UCS2-only driver.
func ValidateShort(text string) error {
	if strings.TrimSpace(text) == "" || !utf8.ValidString(text) {
		return errors.New("短信正文为空或不是有效UTF-8")
	}
	const gsm = "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà"
	const extended = "\f^{}\\[~]|€"
	units, gsmOnly := 0, true
	for _, r := range text {
		if r == 0 || r > 0xffff || r >= 0xd800 && r <= 0xdfff {
			return errors.New("当前短信驱动不接受NUL或非BMP字符（含部分emoji）")
		}
		if strings.ContainsRune(gsm, r) {
			units++
		} else if strings.ContainsRune(extended, r) {
			units += 2
		} else {
			gsmOnly = false
		}
	}
	if gsmOnly && units <= 160 {
		return nil
	}
	if !gsmOnly && utf8.RuneCountInString(text) <= 70 {
		return nil
	}
	return errors.New("超过单条短信长度；发送接口仅允许短短信")
}
