package sms

import (
	"strings"
	"testing"
)

func TestShortSMSUnits(t *testing.T) {
	for _, s := range []string{strings.Repeat("A", 160), strings.Repeat("^", 80), strings.Repeat("中", 70), "中文ABC\n换行", "€"} {
		if e := ValidateShort(s); e != nil {
			t.Errorf("valid %q: %v", s, e)
		}
	}
	for _, s := range []string{"", strings.Repeat("A", 161), strings.Repeat("^", 81), strings.Repeat("中", 71), "🙂", "a\x00b", string([]byte{0xff})} {
		if ValidateShort(s) == nil {
			t.Errorf("invalid %q", s)
		}
	}
}
