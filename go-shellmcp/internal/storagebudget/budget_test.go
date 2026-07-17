package storagebudget

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnforceDeletesOldestFilesFirst(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "old")
	newest := filepath.Join(root, "new")
	if err := os.WriteFile(old, make([]byte, 8), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newest, make([]byte, 8), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if _, err := EnforceLimit(root, 8, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old file still exists: %v", err)
	}
	if _, err := os.Stat(newest); err != nil {
		t.Fatalf("newest removed: %v", err)
	}
}

func TestEnforcePreservesProtectedPath(t *testing.T) {
	root := t.TempDir()
	protected := filepath.Join(root, "current")
	old := filepath.Join(root, "old")
	os.WriteFile(old, make([]byte, 8), 0o600)
	os.WriteFile(protected, make([]byte, 8), 0o600)
	past := time.Now().Add(-time.Hour)
	os.Chtimes(old, past, past)
	result, err := EnforceLimit(root, 4, map[string]bool{protected: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(protected); err != nil {
		t.Fatalf("protected removed: %v", err)
	}
	if result.RemainingBytes != 8 {
		t.Fatalf("remaining=%d", result.RemainingBytes)
	}
}

func TestBudgetIsMinimumOf500MiBAndFivePercent(t *testing.T) {
	if got := LimitForCapacity(20 << 30); got != 500<<20 {
		t.Fatalf("20GiB got=%d", got)
	}
	if got := LimitForCapacity(2 << 30); got != (2<<30)/20 {
		t.Fatalf("2GiB got=%d", got)
	}
}
