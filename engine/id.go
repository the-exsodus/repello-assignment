package engine

import (
	"crypto/rand"
	"fmt"
)

// newID generates a random RFC-4122-shaped (v4) identifier using only the
// standard library, so the project has zero external dependencies. Order
// IDs are always generated server-side -- clients never supply one -- which
// is also why duplicate order IDs cannot occur in practice.
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10xx
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
