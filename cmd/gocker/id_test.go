package main

import (
	"encoding/hex"
	"testing"
)

func TestGenerateContainerID(t *testing.T) {
	id := generateContainerID()
	if len(id) != shortIDLen {
		t.Fatalf("len=%d, want %d (id=%q)", len(id), shortIDLen, id)
	}
	if _, err := hex.DecodeString(id); err != nil {
		t.Fatalf("not hex: %q: %v", id, err)
	}

	other := generateContainerID()
	if id == other {
		t.Fatalf("two calls produced the same id %q", id)
	}
}

func TestShortID(t *testing.T) {
	if got := shortID("abcdef1234567890"); got != "abcdef123456" {
		t.Errorf("shortID(16 hex)=%q", got)
	}
	if got := shortID("abcdef123456"); got != "abcdef123456" {
		t.Errorf("shortID(12)=%q", got)
	}
	if got := shortID("abc"); got != "abc" {
		t.Errorf("shortID(short)=%q", got)
	}
}
