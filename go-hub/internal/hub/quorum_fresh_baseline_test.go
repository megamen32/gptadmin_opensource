package hub

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/megamen32/gptadmin/go-hub/internal/hub/quorum"
)

func TestQuorumSeedsOrdinaryDatabase(t *testing.T) {
	for _, tc := range []struct {
		name        string
		withReceipt bool
	}{{"empty", false}, {"with receipt", true}} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("GPTADMIN_HOME", filepath.Join(root, "home"))
			s := reviewWriter(t, filepath.Join(root, "hub"))
			defer s.Close()
			row := reviewTask(t, "ordinary-receipt")
			if tc.withReceipt {
				if err := s.withTaskDatabase(func(tx *taskTransaction) error {
					return upsertTaskQuorumRow(tx.tx, row.Namespace, row.Key, row.Record.Value, row.Record.Revision)
				}); err != nil {
					t.Fatal(err)
				}
			}
			n := reviewOpenQuorum(t, s, reviewQuorumConfig(t, root))
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if _, err := n.WaitForLeader(ctx); err != nil {
				t.Fatal(err)
			}
			if err := s.seedTaskQuorumBaseline(ctx); err != nil {
				t.Fatalf("bootstrap ordinary database: %v", err)
			}
			if err := s.taskQuorumReplicaReady(); err != nil {
				t.Fatal(err)
			}
			if id := s.memberControlSourceID("successor-member-id"); id != s.authLocalID {
				t.Fatalf("reader would pin %q after election, want enrolled source %q", id, s.authLocalID)
			}
			if tc.withReceipt {
				if err := s.withTaskReadDatabase(func(tx *sql.Tx) error {
					matches, err := taskQuorumBatchMatches(tx, []quorum.Mutation{row})
					if err == nil && !matches {
						t.Error("bootstrapper must install the same receipt revision as its followers")
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
