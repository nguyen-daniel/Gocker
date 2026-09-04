package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

const shortIDLen = 12

// generateContainerID returns a 12-character hex id (48 bits of crypto/rand).
// Display and on-disk names use the same value so `ps` / stop / rm stay aligned.
func generateContainerID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		n := time.Now().UnixNano() & 0xffffffffffff
		return fmt.Sprintf("%012x", n)
	}
	return hex.EncodeToString(b)
}

// shortID returns the 12-character prefix used in CLI output and user-facing resolve.
func shortID(id string) string {
	if len(id) > shortIDLen {
		return id[:shortIDLen]
	}
	return id
}
