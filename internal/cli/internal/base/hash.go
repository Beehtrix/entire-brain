package base

import (
	"crypto/sha256"
	"encoding/hex"
)

func HexSHA(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
