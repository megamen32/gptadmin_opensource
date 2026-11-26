package hub

import (
	"slices"
	"testing"
)

func TestAccessTreeProjection(t *testing.T) {
	profiles := map[string]AccessProfile{}
	for _, id := range []string{"org", "team", "desk", "other"} {
		profiles[id] = AccessProfile{ID: id, Version: 1}
	}
	branches := map[string]accessTargetBranch{
		"org": {}, "team": {Parent: "org"}, "desk": {Parent: "team", Targets: []string{"shell:a"}},
		"other": {Parent: "org", Targets: []string{"shell:b"}},
	}
	plan, err := planAccessTree("org", branches, profiles)
	if err != nil || !slices.Equal(plan.After["org"], []string{"shell:a", "shell:b"}) ||
		!slices.Equal(plan.After["team"], []string{"shell:a"}) || !slices.Equal(plan.After["other"], []string{"shell:b"}) {
		t.Fatalf("incorrect effective targets: %#v %v", plan.After, err)
	}
	profiles["team"] = AccessProfile{ID: "team", Version: 2}
	next, err := planAccessTree("org", branches, profiles)
	if err != nil || next.ETag == plan.ETag {
		t.Fatal("profile edit did not invalidate the plan")
	}
	for _, invalid := range []map[string]accessTargetBranch{
		nil, {"team": {}}, {"org": {Parent: "team"}}, {"org": {}, "desk": {Parent: "missing"}},
		{"org": {}, "desk": {Parent: "team"}, "team": {Parent: "desk"}},
		{"org": {Targets: []string{"*"}}}, {"org": {Targets: []string{"hub"}}},
		{"org": {}, "nonexistent": {Parent: "org"}},
	} {
		if _, err := planAccessTree("org", invalid, profiles); err == nil {
			t.Fatalf("invalid tree accepted: %#v", invalid)
		}
	}
	profiles["desk"] = AccessProfile{ID: "desk", Version: 2, TargetGroup: &accessTargetGroup{Root: "external"}}
	if _, err := planAccessTree("org", branches, profiles); err == nil {
		t.Fatal("other tree taken over")
	}
	profiles["desk"] = AccessProfile{ID: "desk", Version: 3, AllowedTargets: []string{"shell:a"}, TargetGroup: &accessTargetGroup{Root: "org"}}
	delete(branches, "desk")
	plan, err = planAccessTree("org", branches, profiles)
	if err != nil || plan.After["desk"] == nil || len(plan.After["desk"]) != 0 || len(plan.After["team"]) != 0 {
		t.Fatal("removed group retained targets")
	}
}
