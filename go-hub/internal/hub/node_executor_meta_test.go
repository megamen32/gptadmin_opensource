package hub

import (
        "encoding/json"
        "net/http"
        "net/http/httptest"
        "testing"
        "time"
)

// TestMembershipExecutorMetaRoundTrip verifies that a member heartbeat mirrors
// the local executor's capability meta into an existing approved registry
// parent, projects the paired file target, and clears the announcement when
// the member stops advertising it (executor rollback).
func TestMembershipExecutorMetaRoundTrip(t *testing.T) {
        writer := membershipWriterNode(t, "writer")
        ws := httptest.NewServer(writer.Handler())
        defer ws.Close()
        writer.cfg.NodePublicURL = ws.URL
        writer.ensureWriterSelfEntry()
        member, nodeID, key := membershipMemberNode(t, "peer", ws.URL, "")
        ms := httptest.NewServer(member.Handler())
        defer ms.Close()
        grant := membershipIssueGrant(t, writer, "shell:peer")
        if joined := membershipJoin(t, writer, grant, nodeID, key, "shell:peer", ms.URL); joined["__status"] != 200 {
                t.Fatal(joined)
        }
        writer.mu.Lock()
        writer.agents["shell:peer"] = &Agent{AgentID: "shell:peer", Kind: "virtual_shell", Status: "online", Meta: map[string]any{"approved": true, "server_id": "peer-identity"}}
        writer.mu.Unlock()
        member.mu.Lock()
        member.agents["shell:peer"].Meta["file_transfer_version"] = float64(2)
        member.agents["shell:peer"].Meta["build_version"] = float64(1119)
        member.agents["shell:peer"].Meta["git_commit"] = "test-commit"
        member.mu.Unlock()
        client := &http.Client{Timeout: 5 * time.Second}
        member.membershipMemberTick(client, true)
        writer.mu.Lock()
        meta := writer.agents["shell:peer"].Meta
        _, hasFile := writer.exposedAgentByIDLocked("file:peer")
        writer.mu.Unlock()
        if intFromAny(meta["file_transfer_version"]) != 2 || intFromAny(meta["build_version"]) != 1119 || firstString(meta, "git_commit") != "test-commit" {
                t.Fatalf("executor meta not mirrored: %v", meta)
        }
        if !hasFile {
                t.Fatal("paired file target not projected after executor meta")
        }
        if gate := writer.fileTransferVersion("file:peer"); gate != 2 {
                t.Fatalf("paired file gate did not observe capability: %d", gate)
        }
        // Capability rollback: a later heartbeat advertising a pre-contract build
        // (build_version < 194) clears the paired file target.
        member.mu.Lock()
        delete(member.agents["shell:peer"].Meta, "file_transfer_version")
        member.agents["shell:peer"].Meta["build_version"] = float64(150)
        member.mu.Unlock()
        member.membershipMemberTick(client, true)
        writer.mu.Lock()
        _, hasFile = writer.exposedAgentByIDLocked("file:peer")
        writer.mu.Unlock()
        if hasFile || writer.fileTransferVersion("file:peer") != 0 {
                t.Fatal("capability rollback did not clear the paired file target")
        }
}

// TestMembershipExecutorMetaRouteOnlyMember verifies that a member without a
// registry parent on the writer still exposes its capability meta through the
// membership route, projects the paired file target, and passes the
// file_transfer capability gate.
func TestMembershipExecutorMetaRouteOnlyMember(t *testing.T) {
        writer := membershipWriterNode(t, "writer")
        ws := httptest.NewServer(writer.Handler())
        defer ws.Close()
        writer.cfg.NodePublicURL = ws.URL
        writer.ensureWriterSelfEntry()
        member, nodeID, key := membershipMemberNode(t, "peer", ws.URL, "")
        ms := httptest.NewServer(member.Handler())
        defer ms.Close()
        grant := membershipIssueGrant(t, writer, "shell:peer")
        // Route-only enrollment: the join binds the executor identity without a
        // legacy registry parent on the writer.
        body, _ := json.Marshal(nodeJoinRequest{NodeID: nodeID, NodeKey: key, Name: "shell:peer",
                Endpoints: []string{ms.URL}, Build: nodeJoinBuild{GitCommit: "test-commit"}, LocalID: "peer-identity"})
        response := membershipHTTP(writer, http.MethodPost, "/admin/api/nodes/join", grant, body)
        if response.Code != 200 {
                t.Fatalf("join failed: %d %s", response.Code, response.Body.String())
        }
        member.mu.Lock()
        member.agents["shell:peer"].Meta["file_transfer_version"] = float64(2)
        member.agents["shell:peer"].Meta["build_version"] = float64(1119)
        member.mu.Unlock()
        client := &http.Client{Timeout: 5 * time.Second}
        // First tick: joining state, applies the control snapshot afterwards;
        // second tick reports ready and builds the live membership route.
        member.membershipMemberTick(client, true)
        member.membershipMemberTick(client, true)
        writer.mu.Lock()
        _, hasShell := writer.exposedAgentByIDLocked("shell:peer")
        _, hasFile := writer.exposedAgentByIDLocked("file:peer")
        writer.mu.Unlock()
        if !hasShell {
                t.Fatal("route-only member not projected")
        }
        if !hasFile {
                t.Fatal("paired file target not projected for route-only member")
        }
        if gate := writer.fileTransferVersion("file:peer"); gate != 2 {
                t.Fatalf("route capability gate missing: %d", gate)
        }
        writer.membershipMu.Lock()
        stored := writer.membership.Nodes[nodeID].ExecutorMeta
        writer.membershipMu.Unlock()
        if intFromAny(stored["file_transfer_version"]) != 2 || intFromAny(stored["build_version"]) != 1119 {
                t.Fatalf("membership executor meta not stored: %v", stored)
        }
}

// TestValidateNodeExecutorMetaWhitelist verifies bounded validation.
func TestValidateNodeExecutorMetaWhitelist(t *testing.T) {
        if out, err := validateNodeExecutorMeta(nil); err != nil || out != nil {
                t.Fatalf("nil meta rejected: %v %v", out, err)
        }
        if _, err := validateNodeExecutorMeta(map[string]any{"secret": "x"}); err == nil {
                t.Fatal("unknown key accepted")
        }
        if _, err := validateNodeExecutorMeta(map[string]any{"file_transfer_version": "2"}); err == nil {
                t.Fatal("string integer accepted")
        }
        if _, err := validateNodeExecutorMeta(map[string]any{"file_transfer_version": 2.5}); err == nil {
                t.Fatal("fractional integer accepted")
        }
        if _, err := validateNodeExecutorMeta(map[string]any{"os": string(make([]byte, 300))}); err == nil {
                t.Fatal("oversized string accepted")
        }
        out, err := validateNodeExecutorMeta(map[string]any{"file_transfer_version": float64(2), "os": "darwin arm64"})
        if err != nil || out["file_transfer_version"] != 2 || out["os"] != "darwin arm64" {
                t.Fatalf("valid meta rejected: %v %v", out, err)
        }
}
