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

func TestEnforceRootsLimitSharesOneBudgetAcrossDirectories(t *testing.T) {
	base := t.TempDir()
	spool := filepath.Join(base, "spool")
	audit := filepath.Join(base, "audit")
	if err := os.MkdirAll(spool, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(audit, 0o700); err != nil {
		t.Fatal(err)
	}
	oldest := filepath.Join(audit, "audit.jsonl")
	middle := filepath.Join(spool, "result-old.json")
	newest := filepath.Join(spool, "result-new.json")
	for _, path := range []string{oldest, middle, newest} {
		if err := os.WriteFile(path, make([]byte, 8), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	if err := os.Chtimes(oldest, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(middle, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	result, err := EnforceRootsLimit([]string{spool, audit}, 16, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.RemainingBytes != 16 || result.RemovedFiles != 1 {
		t.Fatalf("result=%+v", result)
	}
	if _, err := os.Stat(oldest); !os.IsNotExist(err) {
		t.Fatalf("globally oldest file remains: %v", err)
	}
	if _, err := os.Stat(middle); err != nil {
		t.Fatalf("newer spool file removed: %v", err)
	}
}

func TestEnforceRootsLimitDoesNotDoubleCountNestedRoots(t *testing.T) {
	root := t.TempDir()
	outbox := filepath.Join(root, "outbox")
	if err := os.MkdirAll(outbox, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(outbox, "result.json")
	if err := os.WriteFile(path, make([]byte, 8), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := EnforceRootsLimit([]string{root, outbox}, 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.RemainingBytes != 8 || result.RemovedFiles != 0 {
		t.Fatalf("nested root was counted twice: %+v", result)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file removed after double counting: %v", err)
	}
}

func TestEnforceRootsLimitCanOwnOneFileWithoutOwningItsDirectory(t *testing.T) {
	base := t.TempDir()
	spool := filepath.Join(base, "spool")
	logs := filepath.Join(base, "logs")
	if err := os.MkdirAll(spool, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	audit := filepath.Join(logs, "shellmcp-audit.jsonl")
	unrelated := filepath.Join(logs, "unrelated-service.log")
	spoolFile := filepath.Join(spool, "result.json")
	for _, path := range []string{audit, unrelated, spoolFile} {
		if err := os.WriteFile(path, make([]byte, 8), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(audit, past, past); err != nil {
		t.Fatal(err)
	}

	result, err := EnforceRootsLimit([]string{spool, audit}, 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.RemainingBytes != 8 || result.RemovedFiles != 1 {
		t.Fatalf("result=%+v", result)
	}
	if _, err := os.Stat(audit); !os.IsNotExist(err) {
		t.Fatalf("owned audit file remains: %v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated sibling was removed: %v", err)
	}
}
