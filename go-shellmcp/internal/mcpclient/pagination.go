package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	toolsListMaxPages = 64
	toolsListMaxTools = 4096
	toolsListMaxBytes = 8 << 20
	toolsListTimeout  = 30 * time.Second
)

// listToolsPages returns a complete catalog or an error, never a successful
// partial catalog. Budgets cover the whole enumeration, including empty pages.
func listToolsPages(ctx context.Context, request func(context.Context, map[string]any) (map[string]any, error)) ([]map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, toolsListTimeout)
	defer cancel()

	out := make([]map[string]any, 0)
	params := map[string]any{}
	seen := make(map[string]struct{})
	totalBytes := 0
	for page := 0; page < toolsListMaxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result, err := request(ctx, params)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return nil, fmt.Errorf("mcp child: tools/list result encoding: %w", err)
		}
		totalBytes += len(encoded)
		if totalBytes > toolsListMaxBytes {
			return nil, fmt.Errorf("mcp child: tools/list byte limit exceeded (%d)", toolsListMaxBytes)
		}
		raw, ok := result["tools"].([]any)
		if !ok {
			return nil, errors.New("mcp child: tools/list result has no tools array")
		}
		if len(out)+len(raw) > toolsListMaxTools {
			return nil, fmt.Errorf("mcp child: tools/list tool limit exceeded (%d)", toolsListMaxTools)
		}
		for _, item := range raw {
			tool, ok := item.(map[string]any)
			if !ok {
				return nil, errors.New("mcp child: tools/list result contains a non-object tool")
			}
			name, named := tool["name"].(string)
			if !named || strings.TrimSpace(name) == "" {
				return nil, errors.New("mcp child: tools/list result contains a tool with an invalid name")
			}
			out = append(out, tool)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		next, more := result["nextCursor"]
		if !more {
			return out, nil
		}
		cursor, ok := next.(string)
		if !ok {
			return nil, errors.New("mcp child: tools/list next cursor is not a string")
		}
		if _, repeated := seen[cursor]; repeated {
			return nil, errors.New("mcp child: tools/list repeated cursor")
		}
		seen[cursor] = struct{}{}
		// Cursors are opaque: preserve even empty strings byte-for-byte.
		params = map[string]any{"cursor": cursor}
	}
	return nil, fmt.Errorf("mcp child: tools/list page limit exceeded (%d)", toolsListMaxPages)
}
