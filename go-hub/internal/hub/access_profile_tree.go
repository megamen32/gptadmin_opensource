package hub

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
)

// The tree is configuration for the existing allow lists. Direct membership is
// retained so an operator can reconstruct the tree and a later edit can revoke
// removed branches. Enforcement remains in the normal access-profile path.
type accessTargetBranch struct {
	Parent  string   `json:"parent,omitempty"`
	Targets []string `json:"targets"`
}

type accessTargetGroup struct {
	Root string `json:"root"`
	accessTargetBranch
}

type accessTreePlan struct {
	Before map[string][]string `json:"before"`
	After  map[string][]string `json:"after"`
	ETag   string              `json:"etag"`
}

func planAccessTree(root string, branches map[string]accessTargetBranch, profiles map[string]AccessProfile) (accessTreePlan, error) {
	plan := accessTreePlan{Before: map[string][]string{}, After: map[string][]string{}}
	if branches == nil || len(branches) > accessProfileMaxListItems {
		return plan, fmt.Errorf("provide a groups object with up to %d existing profiles", accessProfileMaxListItems)
	}
	if _, ok := branches[root]; !ok && len(branches) != 0 {
		return plan, fmt.Errorf("groups must include root profile %q", root)
	}
	children := make(map[string][]string)
	for id, b := range branches {
		p, ok := profiles[id]
		if !ok || id == "" || (p.TargetGroup != nil && p.TargetGroup.Root != root) {
			return plan, fmt.Errorf("unknown profile or profile already in another tree: %q", id)
		}
		if id == root {
			if b.Parent != "" {
				return plan, fmt.Errorf("root profile must not have a parent")
			}
		} else {
			if _, ok := branches[b.Parent]; !ok {
				return plan, fmt.Errorf("unknown parent %q for %q", b.Parent, id)
			}
			children[b.Parent] = append(children[b.Parent], id)
		}
		for _, target := range b.Targets {
			kind, name, found := strings.Cut(target, ":")
			if !found || name == "" || strings.TrimSpace(target) != target || strings.ContainsAny(target, "*\r\n\x00") || len(target) > accessProfileMaxStringBytes || !slices.Contains([]string{"shell", "file", "mcp", "control"}, kind) {
				return plan, fmt.Errorf("group %q requires concrete device targets; invalid %q", id, target)
			}
		}
	}
	// Each non-root has exactly one parent. Nodes outside the root's subtree
	// necessarily contain a cycle, so we never traverse a cycle recursively.
	var visit func(string) error
	visit = func(id string) error {
		targets := slices.Clone(branches[id].Targets)
		for _, child := range children[id] {
			if err := visit(child); err != nil {
				return err
			}
			targets = append(targets, plan.After[child]...)
		}
		sort.Strings(targets)
		targets = slices.Compact(targets)
		if len(targets) > accessProfileMaxListItems {
			return fmt.Errorf("too many targets in subtree %q", id)
		}
		plan.After[id] = append([]string{}, targets...)
		return nil
	}
	if len(branches) > 0 {
		if err := visit(root); err != nil {
			return plan, err
		}
		if len(plan.After) != len(branches) {
			return plan, fmt.Errorf("groups contain a cycle disconnected from the root")
		}
	}
	versions := make(map[string]int64)
	for id, p := range profiles {
		_, included := branches[id]
		if !included && (p.TargetGroup == nil || p.TargetGroup.Root != root) {
			continue
		}
		versions[id] = p.Version
		plan.Before[id] = slices.Clone(p.AllowedTargets)
		if !included {
			plan.After[id] = []string{}
		}
	}
	raw, err := json.Marshal([]any{root, branches, versions})
	if err != nil {
		return plan, err
	}
	plan.ETag = fmt.Sprintf(`"%x"`, sha256.Sum256(raw))
	return plan, nil
}

func (s *Server) adminAccessProfileTree(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	root := strings.TrimPrefix(r.URL.Path, "/admin/api/access-profile-trees/")
	if root == "" || strings.Contains(root, "/") {
		writeJSON(w, 400, map[string]any{"detail": "one root profile id is required"})
		return
	}
	action := "plan_tree"
	switch r.Method {
	case http.MethodPost:
	case http.MethodPut:
		action = "apply_tree"
		if r.Header.Get("If-Match") == "" {
			writeJSON(w, 428, map[string]any{"detail": "plan_tree etag is required"})
			return
		}
	default:
		w.Header().Set("Allow", "POST, PUT")
		writeJSON(w, 405, map[string]any{"detail": "use POST to plan or PUT to apply"})
		return
	}
	if err := authorizeToolAction(r, "hub", "access_profiles", map[string]any{"action": action}); err != nil {
		writeJSON(w, 403, map[string]any{"detail": err.Error()})
		return
	}
	var body struct {
		Groups map[string]accessTargetBranch `json:"groups"`
	}
	if err := readBoundedJSON(r, &body); err != nil {
		writeJSON(w, 400, map[string]any{"detail": err.Error()})
		return
	}
	var plan accessTreePlan
	var err error
	unchanged := errors.New("tree already applied")
	compute := func(profiles map[string]AccessProfile) error {
		var e error
		plan, e = planAccessTree(root, body.Groups, profiles)
		if e != nil {
			return &accessProfileHTTPError{400, e.Error()}
		}
		return nil
	}
	if action == "plan_tree" {
		var profiles map[string]AccessProfile
		profiles, err = s.accessProfilesSnapshot()
		if err == nil {
			err = compute(profiles)
		}
	} else {
		_, err = s.updateAccessProfiles(func(profiles map[string]AccessProfile) error {
			if e := compute(profiles); e != nil {
				return e
			}
			if r.Header.Get("If-Match") != plan.ETag {
				return &accessProfileHTTPError{412, "tree plan or access profiles changed; plan_tree again"}
			}
			now := time.Now().UTC()
			changed := false
			for id, targets := range plan.After {
				current := profiles[id]
				p := cloneAccessProfile(current)
				p.TargetGroup = nil
				if b, present := body.Groups[id]; present {
					p.TargetGroup = &accessTargetGroup{root, b}
				}
				if sameTargetGroup(current.TargetGroup, p.TargetGroup) && slices.Equal(current.AllowedTargets, targets) {
					continue
				}
				changed = true
				p.AllowedTargets = targets
				p.Version++
				p.UpdatedAt = now
				profiles[id] = s.pinAccessProfileTargets(p, current)
			}
			if !changed {
				return unchanged
			}
			// Check the size limit before the control-state commit.
			encoded, e := json.MarshalIndent(accessProfilesState{Profiles: profiles}, "", "  ")
			if e != nil || len(encoded)+1 > accessProfileStateMaxBytes {
				return &accessProfileHTTPError{400, "result exceeds access profile storage limit"}
			}
			after, e := planAccessTree(root, body.Groups, profiles)
			if e != nil {
				return e
			}
			plan.ETag = after.ETag
			return nil
		})
	}
	if err != nil && err != unchanged {
		writeAccessProfileError(w, err)
		return
	}
	w.Header().Set("ETag", plan.ETag)
	writeJSON(w, 200, plan)
}

func sameTargetGroup(a, b *accessTargetGroup) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Root == b.Root && a.Parent == b.Parent && slices.Equal(a.Targets, b.Targets)
}
