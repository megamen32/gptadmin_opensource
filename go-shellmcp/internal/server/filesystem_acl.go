package server

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/fsacl"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/hub"
)

type filesystemACLContextKey struct{}

func withFilesystemACL(ctx context.Context, policy *hub.FilesystemACL) context.Context {
	return context.WithValue(ctx, filesystemACLContextKey{}, policy)
}

func filesystemACLFromContext(ctx context.Context) (*hub.FilesystemACL, bool) {
	policy, ok := ctx.Value(filesystemACLContextKey{}).(*hub.FilesystemACL)
	return policy, ok && policy != nil
}

func filesystemScopeFromContext(ctx context.Context) (*fsacl.Policy, bool) {
	policy, ok := filesystemACLFromContext(ctx)
	if !ok {
		return nil, false
	}
	return &fsacl.Policy{ReadRoots: policy.ReadRoots, WriteRoots: policy.WriteRoots, RequireCanonicalRoots: true}, true
}

func sameFilesystemACL(a, b *hub.FilesystemACL) bool {
	return a != nil && b != nil && reflect.DeepEqual(a, b)
}

func filesystemACLTargetsExecutor(target, executor string) bool {
	for _, prefix := range []string{"file:", "shell:"} {
		if strings.HasPrefix(target, prefix) {
			name := strings.TrimSpace(strings.TrimPrefix(target, prefix))
			return name != "" && !strings.ContainsAny(name, "/\\") && canonicalACLExecutorName(name) == canonicalACLExecutorName(executor)
		}
	}
	return false
}

func canonicalACLExecutorName(name string) string {
	name = strings.TrimSpace(name)
	switch strings.ToLower(name) {
	case "server01", "home-assistant":
		return "haos"
	default:
		return name
	}
}

func requireNoFilesystemACL(ctx context.Context, tool string) error {
	if _, active := filesystemACLFromContext(ctx); active {
		return errors.New("filesystem-bound callback does not permit " + tool)
	}
	return nil
}

func filesystemACLMetaForChildCall(ctx context.Context, ref, tool string) (map[string]any, error) {
	acl, active := filesystemACLFromContext(ctx)
	if !active {
		return nil, nil
	}
	if !strings.EqualFold(strings.TrimSpace(ref), "grepmesh") ||
		(tool != "search" && tool != "search_text" && tool != "read_text") ||
		acl.ReadRoots == nil || len(*acl.ReadRoots) == 0 {
		return nil, errors.New("filesystem-bound child call permits only GrepMesh reads within non-empty read_roots")
	}
	return map[string]any{"io.gptadmin/filesystem-acl": acl}, nil
}
