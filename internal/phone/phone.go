// Package phone canonicalizes user input without rewriting short service codes.
package phone

import "strings"

// Normalize accepts an optional country prefix for mainland mobile numbers.
// Other international numbers must keep their explicit '+'. Service codes keep
// their original digits. An empty result means the input is invalid/unknown.
func Normalize(value string) string {
	s := strings.TrimSpace(value)
	digits := strings.TrimPrefix(s, "+")
	if len(digits) < 5 || len(digits) > 20 || strings.Trim(digits, "0") == "" {
		return ""
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return ""
		}
	}
	if strings.HasPrefix(s, "+") {
		return s
	}
	if mainland(digits) {
		return "+86" + digits
	}
	if len(digits) == 13 && strings.HasPrefix(digits, "86") && mainland(digits[2:]) {
		return "+" + digits
	}
	return digits
}

func mainland(s string) bool { return len(s) == 11 && s[0] == '1' && s[1] >= '3' && s[1] <= '9' }
func Equal(a, b string) bool { n := Normalize(a); return n != "" && n == Normalize(b) }
