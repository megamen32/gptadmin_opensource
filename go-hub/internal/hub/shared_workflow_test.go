package hub

import (
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestSharedWorkflowPreservesDefaultAndNamedProfiles(t *testing.T) {
	s, caller, path := instructionFixture(t)
	named := "  Unique admin restrictions: keep owner STOP.\n"
	if err := s.updateInstructionSets(func(sets map[string]InstructionSet) error {
		sets["admin-only"] = InstructionSet{ID: "admin-only", Content: named, Version: instructionSetVersion(named)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	requests := []*http.Request{caller, requestWithAccessProfile(caller, AccessProfile{
		ID: "restricted", InstructionSetID: "admin-only", Version: 7,
		AccessMode: accessModeFull, AllowedTargets: []string{"hub"}, AllowedTools: []string{"status"},
	})}
	rawDefault, _ := os.ReadFile(path)
	for i, req := range requests {
		want := string(rawDefault)
		if i == 1 {
			want = named
		}
		set, _, err := s.instructionDocument(req)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(set.Content, want+"\n\n") || strings.Count(set.Content, "<!-- gptadmin-shared-workflow -->") != 1 {
			t.Fatalf("profile changed or duplicate composition: %q", set.Content)
		}
		if !strings.Contains(set.Content, sharedWorkflowInstructions) || set.Version != instructionSetVersion(set.Content) {
			t.Fatal("effective version excludes shared workflow")
		}
		if s.startupInstructionsTextForRequest(req) != set.Content {
			t.Fatal("pluginless delivery diverges")
		}
		again, _, err := s.instructionDocument(req)
		if err != nil || again.Content != set.Content {
			t.Fatal("repeat delivery accumulates components")
		}
	}
	persisted, _ := s.namedInstructionSetSnapshot("admin-only")
	afterDefault, _ := os.ReadFile(path)
	if persisted.Content != named || string(afterDefault) != string(rawDefault) {
		t.Fatal("persisted profile mutated")
	}
	profile, _ := AccessProfileFromRequest(requests[1])
	if profile.Version != 7 || len(profile.AllowedTools) != 1 || profile.AllowedTools[0] != "status" {
		t.Fatal("ACL changed")
	}
}

func TestSharedWorkflowRevisionInvalidatesSignedAndSessionReceipts(t *testing.T) {
	s, r, _ := instructionFixture(t)
	s.cfg.InstructionSessionReceipts = true
	raw := s.instructionSetForRequest(r)
	old, err := composeSharedWorkflow(raw, "Prior shared workflow revision.")
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.issueInstructionContext(r, old)
	if err != nil {
		t.Fatal(err)
	}
	stale := requestWithInstructionContext(r, map[string]any{"context": token})
	s.rememberInstructionReceipt(r, old)
	if s.validInstructionToken(stale) || s.hasInstructionReceipt(r) {
		t.Fatal("old shared revision receipt accepted")
	}
	out := instructionDiscovery(t, s, r)
	current, _, err := s.instructionDocument(r)
	if err != nil {
		t.Fatal(err)
	}
	if out["instruction_version"] != current.Version || out["shared_workflow_version"] != instructionSetVersion(sharedWorkflowInstructions) {
		t.Fatal("delivery version trace missing")
	}
	if !s.validInstructionContext(requestWithInstructionContext(r, map[string]any{"context": out["context"]})) {
		t.Fatal("fresh effective receipt rejected")
	}
}

func TestSharedWorkflowPreservesFullProfileBudgetAndRejectsInvalidComponent(t *testing.T) {
	raw := InstructionSet{ID: "default", Content: strings.Repeat("p", startupInstructionsMaxBytes)}
	effective, err := composeSharedWorkflow(raw, sharedWorkflowInstructions)
	if err != nil || !strings.HasPrefix(effective.Content, raw.Content) || len(effective.Content) > effectiveInstructionsMaxBytes {
		t.Fatal("existing max-size profile truncated or rejected", err)
	}
	for _, shared := range []string{"", strings.Repeat("s", sharedWorkflowMaxBytes+1), string([]byte{0xff})} {
		if _, err := composeSharedWorkflow(raw, shared); err == nil {
			t.Fatal("invalid shared component accepted")
		}
	}
	raw.Content += "x"
	if _, err := composeSharedWorkflow(raw, sharedWorkflowInstructions); err == nil {
		t.Fatal("oversize persisted profile accepted")
	}
}

func TestSharedWorkflowMissingNamedProfileNeverDeliversDefault(t *testing.T) {
	s, r, _ := instructionFixture(t)
	r = requestWithAccessProfile(r, AccessProfile{ID: "missing", InstructionSetID: "absent", Version: 1})
	if _, _, err := s.instructionDocument(r); err == nil {
		t.Fatal("missing named profile accepted")
	}
	text := s.startupInstructionsTextForRequest(r)
	if strings.Contains(text, "Fixture instructions") || strings.Contains(text, sharedWorkflowInstructions) {
		t.Fatal("missing named profile silently substituted")
	}
}

// Literal transport contract, separate from production composition.
func expectedSharedInstructions(profile string) string {
	return profile + "\n\n<!-- gptadmin-shared-workflow -->\n" + sharedWorkflowInstructions
}
