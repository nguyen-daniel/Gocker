//go:build linux

package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveID(t *testing.T) {
	testID := "1234567890123456789"
	testState := &ContainerState{
		ID:        testID,
		PID:       12345,
		Status:    "exited",
		CreatedAt: time.Now(),
		Command:   []string{"/bin/sh"},
	}

	if err := EnsureDir(); err != nil {
		t.Fatalf("Failed to ensure state dir: %v", err)
	}

	if err := Save(testState); err != nil {
		t.Fatalf("Failed to save test state: %v", err)
	}

	defer func() {
		os.Remove(filepath.Join(ContainersDir, testID+".json"))
	}()

	resolved, err := ResolveID(testID)
	if err != nil {
		t.Errorf("Failed to resolve full ID: %v", err)
	}
	if resolved != testID {
		t.Errorf("Expected %s, got %s", testID, resolved)
	}

	resolved, err = ResolveID("123456")
	if err != nil {
		t.Errorf("Failed to resolve partial ID: %v", err)
	}
	if resolved != testID {
		t.Errorf("Expected %s, got %s", testID, resolved)
	}

	_, err = ResolveID("nonexistent")
	if err == nil {
		t.Errorf("Expected error for non-existent ID, got nil")
	}
}

func TestResolveIDByName(t *testing.T) {
	testID := "namedcontainer999888777"
	testState := &ContainerState{
		ID:        testID,
		Name:      "demo-ctr",
		PID:       1,
		Status:    "exited",
		CreatedAt: time.Now(),
		Command:   []string{"/bin/sh"},
	}

	if err := EnsureDir(); err != nil {
		t.Fatalf("Failed to ensure state dir: %v", err)
	}
	if err := Save(testState); err != nil {
		t.Fatalf("Failed to save test state: %v", err)
	}
	defer os.Remove(filepath.Join(ContainersDir, testID+".json"))

	resolved, err := ResolveID("demo-ctr")
	if err != nil {
		t.Fatalf("resolve by name: %v", err)
	}
	if resolved != testID {
		t.Errorf("got %s, want %s", resolved, testID)
	}

	taken, err := NameTaken("demo-ctr")
	if err != nil {
		t.Fatalf("NameTaken: %v", err)
	}
	if !taken {
		t.Error("expected NameTaken(\"demo-ctr\")")
	}
	taken, err = NameTaken("other-name")
	if err != nil {
		t.Fatalf("NameTaken: %v", err)
	}
	if taken {
		t.Error("did not expect NameTaken(\"other-name\")")
	}
}
