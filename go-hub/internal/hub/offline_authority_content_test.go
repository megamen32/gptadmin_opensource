package hub

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Build real SQLite/FTS stores with byte-identical common references. Only the
// incoming snapshot gains references after the target snapshot is taken.
func offlineContentSubsetFixture(t *testing.T, target, incoming string) (ContentSource, ContentSource, ContentSource) {
	t.Helper()
	store, err := OpenContentStore(incoming, defaultExecContentLimits)
	if err != nil {
		t.Fatal(err)
	}
	common, err := store.Index("original-principal", "common", "shell:writer", "shared searchable content\nsecond line")
	if err != nil {
		t.Fatal(err)
	}
	common, err = store.Source("original-principal", common.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := handoffCopyFile(incoming, target); err != nil {
		t.Fatal(err)
	}
	store, err = OpenContentStore(incoming, defaultExecContentLimits)
	if err != nil {
		t.Fatal(err)
	}
	extra, err := store.SaveIncompleteFormatted("original-principal", "incoming-only", "shell:writer", "stdout", "preserved partial result")
	if err != nil {
		t.Fatal(err)
	}
	extra, err = store.Source("original-principal", extra.ID)
	if err != nil {
		t.Fatal(err)
	}
	expired, err := store.Index("historical-principal", "expired", "shell:writer", "historical searchable content")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE content_sources SET expires=? WHERE id=?`, time.Now().Add(-time.Hour).Unix(), expired.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return common, extra, expired
}

func offlineContentSQL(t *testing.T, path, query string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestOfflineAuthorityContentSubsetStrictComparison(t *testing.T) {
	for _, test := range []struct {
		name  string
		query string
	}{
		{"metadata", `UPDATE content_sources SET name='changed' WHERE id=?`},
		{"principal", `UPDATE content_sources SET principal='different-principal' WHERE id=?`},
		{"completeness", `UPDATE content_sources SET complete=0 WHERE id=?`},
		{"chunk", `UPDATE content_chunks SET body='changed chunk' WHERE source_id=?`},
		{"chunk_principal", `UPDATE content_chunks SET principal='different-principal' WHERE source_id=?`},
		{"fts", `UPDATE content_fts SET body='changed search text' WHERE source_id=?`},
		{"fts_duplicate", `INSERT INTO content_fts(source_id,principal,chunk_id,body) SELECT source_id,principal,chunk_id,body FROM content_fts WHERE source_id=?`},
		{"target_only", `UPDATE content_sources SET id='target-only-reference' WHERE id=?`},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			target, incoming := filepath.Join(dir, "target.sqlite"), filepath.Join(dir, "incoming.sqlite")
			common, _, _ := offlineContentSubsetFixture(t, target, incoming)
			offlineContentSQL(t, target, test.query, common.ID)
			before, _ := handoffFileDigest(target)
			digest, _ := handoffFileDigest(incoming)
			_, _, err := handoffStageWriterContent(target, incoming, filepath.Join(dir, "staged.sqlite"), digest, time.Now().Unix())
			if err == nil || !strings.Contains(err.Error(), "distinct active content") {
				t.Fatalf("conflicting reference accepted: %v", err)
			}
			if after, _ := handoffFileDigest(target); after != before {
				t.Fatal("refusal changed the target content store")
			}
			if _, err := os.Stat(filepath.Join(dir, "staged.sqlite")); !os.IsNotExist(err) {
				t.Fatal("conflicting content was staged")
			}
		})
	}
}

func TestOfflineAuthorityContentSubsetRetainAndWAL(t *testing.T) {
	for _, mode := range []string{"equal", "empty_wal", "nonempty_wal", "incoming_digest"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			target, incoming, staged := filepath.Join(dir, "target.sqlite"), filepath.Join(dir, "incoming.sqlite"), filepath.Join(dir, "staged.sqlite")
			common, _, _ := offlineContentSubsetFixture(t, target, incoming)
			if mode == "equal" {
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				if err := handoffCopyFile(incoming, target); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "empty_wal" {
				if err := os.WriteFile(target+"-wal", nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "nonempty_wal" {
				db, err := sql.Open("sqlite", target)
				if err != nil {
					t.Fatal(err)
				}
				db.SetMaxOpenConns(1)
				defer db.Close()
				if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`PRAGMA wal_autocheckpoint=0`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE content_sources SET name='temporary WAL update' WHERE id=?`, common.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE content_sources SET name=? WHERE id=?`, common.Name, common.ID); err != nil {
					t.Fatal(err)
				}
				if info, err := os.Stat(target + "-wal"); err != nil || info.Size() == 0 {
					t.Fatal("fixture did not create a real nonempty SQLite WAL")
				}
			}
			before, _ := handoffFileDigest(target)
			digest, _ := handoffFileDigest(incoming)
			if mode == "incoming_digest" {
				digest = strings.Repeat("0", 64)
			}
			retain, guards, err := handoffStageWriterContent(target, incoming, staged, digest, time.Now().Unix())
			if mode == "nonempty_wal" || mode == "incoming_digest" {
				want := "WAL is not empty"
				if mode == "incoming_digest" {
					want = "differs from the verified incoming bundle"
				}
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("unsafe replacement accepted: %v", err)
				}
			} else if err != nil || retain != (mode == "equal") || len(guards) != 2 || guards[0].Digest != before {
				t.Fatalf("unexpected staging result: retain=%v guards=%+v err=%v", retain, guards, err)
			}
			if mode == "equal" {
				if _, err := os.Stat(staged); !os.IsNotExist(err) {
					t.Fatal("equal active content was replaced")
				}
			}
			if mode == "empty_wal" {
				if actual, _ := handoffFileDigest(staged); actual != digest || guards[1].Digest == "" {
					t.Fatal("subset staging did not preserve the full incoming snapshot or empty-WAL guard")
				}
			}
			if after, _ := handoffFileDigest(target); after != before {
				t.Fatal("preparation changed the target content store")
			}
		})
	}
}

func TestOfflineAuthorityContentSubsetPrepareApply(t *testing.T) {
	for _, drift := range []string{"none", "database", "wal"} {
		t.Run(drift, func(t *testing.T) {
			source := offlineAuthorityFixture(t)
			target := offlineHandoffReader(t, "reader", "reader-identity", "member-44", "node44-key")
			snapshot := authContinuityHTTP(source, "POST", "/admin/api/auth-snapshot/export", "fixture-owner", nil)
			if got := authContinuityHTTP(target, "POST", "/admin/api/auth-snapshot/apply", "fixture-owner", snapshot.Body.Bytes()); got.Code != 200 {
				t.Fatalf("reader seed: %d", got.Code)
			}
			targetPath, sourcePath := filepath.Join(target.cfg.ConfigDir, "exec-content.sqlite"), filepath.Join(source.cfg.ConfigDir, "exec-content.sqlite")
			common, extra, expired := offlineContentSubsetFixture(t, targetPath, sourcePath)
			if err := source.Close(); err != nil {
				t.Fatal(err)
			}
			if err := target.Close(); err != nil {
				t.Fatal(err)
			}
			before, _ := handoffFileDigest(targetPath)
			bundle, err := ExportStoppedAuthority(source.cfg, AuthorityHandoffExportOptions{SourceTarget: "shell:writer", ExpectedWriter: "writer-identity", OutputDir: filepath.Join(t.TempDir(), "bundle")})
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := PrepareStoppedAuthorityWriter(target.cfg, AuthorityHandoffPrepareOptions{BundleDir: bundle.Directory, BundleDigest: bundle.BundleDigest, Target: "shell:reader", ExpectedLocalID: "reader-identity", SourcePeerURL: "https://hub.example", SourceConnectTo: "192.0.2.100", OutputDir: filepath.Join(t.TempDir(), "stage")})
			if err != nil {
				t.Fatal(err)
			}
			plan, err := handoffReadPlan(prepared.Directory, prepared.PlanDigest)
			if err != nil {
				t.Fatal(err)
			}
			incoming, _ := handoffFileDigest(filepath.Join(bundle.Directory, "exec-content.sqlite"))
			contentPlanned := false
			for _, file := range plan.Files {
				if file.Relative == "exec-content.sqlite" {
					contentPlanned = file.Before == before && file.After == incoming
				}
			}
			guards := map[string]string{}
			for _, guard := range plan.Guards {
				guards[guard.Path] = guard.Digest
			}
			_, walGuarded := guards[targetPath+"-wal"]
			_, mainImmutableGuarded := guards[targetPath]
			if !contentPlanned || mainImmutableGuarded || !walGuarded || guards[targetPath+"-wal"] != "" {
				t.Fatal("content replacement or original database/WAL guard missing from real prepare plan")
			}
			authPath := filepath.Join(target.cfg.ConfigDir, "auth-continuity", "state.json")
			authBefore, _ := handoffFileDigest(authPath)
			if drift == "database" {
				offlineContentSQL(t, targetPath, `UPDATE content_sources SET name='concurrent change' WHERE id=?`, common.ID)
			} else if drift == "wal" {
				if err := os.WriteFile(targetPath+"-wal", nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			backup := filepath.Join(t.TempDir(), "backup")
			_, err = ApplyStoppedAuthority(target.cfg, AuthorityHandoffApplyOptions{PreparedDir: prepared.Directory, PlanDigest: prepared.PlanDigest, BackupDir: backup})
			if drift != "none" {
				if err == nil {
					t.Fatal("apply accepted original content database/WAL drift")
				}
				if after, _ := handoffFileDigest(authPath); after != authBefore {
					t.Fatal("failed CAS changed authority")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if after, _ := handoffFileDigest(targetPath); after != incoming {
				t.Fatal("apply did not install the complete verified incoming snapshot")
			}
			db, err := openTaskReadDatabase(targetPath)
			if err != nil {
				t.Fatal(err)
			}
			var expiredRows int
			err = db.QueryRow(`SELECT count(*) FROM content_sources WHERE id=? AND expires<?`, expired.ID, time.Now().Unix()).Scan(&expiredRows)
			db.Close()
			if err != nil || expiredRows != 1 {
				t.Fatal("full snapshot copy dropped incoming expired history")
			}
			// Public reads expire old rows. Exercise those reads on a copy so
			// they do not deliberately modify the original apply/restore CAS.
			readPath := filepath.Join(t.TempDir(), "api-content.sqlite")
			if err := handoffCopyFile(targetPath, readPath); err != nil {
				t.Fatal(err)
			}
			store, err := OpenContentStore(readPath, defaultExecContentLimits)
			if err != nil {
				t.Fatal(err)
			}
			for _, expected := range []ContentSource{common, extra} {
				got, err := store.Source("original-principal", expected.ID)
				if err != nil || got != expected {
					t.Fatalf("content identity/TTL/completeness changed: %+v %v", got, err)
				}
				if _, err := store.Source("foreign-principal", expected.ID); err == nil {
					t.Fatal("content principal isolation lost")
				}
			}
			if hits, err := store.Search("original-principal", common.ID, "searchable", 5); err != nil || len(hits) == 0 {
				t.Fatalf("existing FTS reference lost: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if err := RestoreStoppedAuthorityBeforeActivation(target.cfg, backup, prepared.PlanDigest); err != nil {
				t.Fatal(err)
			}
			if restored, _ := handoffFileDigest(targetPath); restored != before {
				t.Fatal("preactivation rollback did not restore the original content database")
			}
		})
	}
}
