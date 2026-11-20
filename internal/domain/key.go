package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func Key(parts ...string) string {
	b, _ := json.Marshal(parts)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}
