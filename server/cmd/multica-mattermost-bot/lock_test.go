//go:build unix

package main

import (
	"path/filepath"
	"testing"
)

func TestAcquireLock_SecondInstanceRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db.lock")

	release1, err := acquireLock(path)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release1()

	if _, err := acquireLock(path); err == nil {
		t.Fatal("second acquire should fail — flock already held")
	}
}

func TestAcquireLock_ReleaseAllowsReacquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db.lock")

	release1, err := acquireLock(path)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	release1()

	release2, err := acquireLock(path)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	release2()
}
