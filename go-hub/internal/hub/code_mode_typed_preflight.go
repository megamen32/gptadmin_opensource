package hub

import (
	"fmt"
	"net/http"

	"github.com/fastschema/qjs"
)

// prepared schemas are trusted node-discovered MCP contracts, never
// tool-defined content supplied back by the client to bypass validation.
func (b *codeModeBridge) cachePreparedSchema(target, tool string, schema any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.preparedSchemas == nil {
		b.preparedSchemas = make(map[string]any)
	}
	b.preparedSchemas[target+"\x00"+tool] = schema
}

func (b *codeModeBridge) preparedSchema(target, tool string) (any, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	value, exists := b.preparedSchemas[target+"\x00"+tool]
	return value, exists
}

// prepareTool is the zero-side-effect preflight behind tools[target].tool(args).
// It reads the caller-authorized MCP schema and checks full JSON Schema types
// before dispatch. It does NOT create a task or put an operation on a queue.
func (b *codeModeBridge) prepareTool(this *qjs.This) (*qjs.Value, error) {
	if err := b.entry(); err != nil {
		return nil, err
	}
	if _, err := b.remaining(); err != nil {
		return nil, err
	}
	target, tool := codeModeStringArg(this, 0), codeModeStringArg(this, 1)
	args := codeModeObjectArg(this, 2)
	if target == "" || tool == "" {
		return nil, b.fail("invalid_arguments", "tools[] requires a target and tool name", nil)
	}
	request := b.request.WithContext(b.ctx)
	if err := authorizeToolAction(request, target, tool, args); err != nil {
		return nil, b.fail("forbidden", err.Error(), nil)
	}
	schema, found := b.preparedSchema(target, tool)
	if !found {
		if static, known := builtInMCPInputSchema(target, tool); known {
			schema = static
		} else {
			result := mapValue(b.server.appsSDKSchemaForRequest(request, map[string]any{"target": target}))
			if firstString(result, "status") != "completed" {
				return nil, b.fail("schema_unavailable", fmt.Sprintf("cannot read MCP inputSchema for %s.%s; no tool was executed", target, tool), nil)
			}
			for _, item := range outputRows(mapValue(result["response"])["tools"]) {
				definition := mapValue(item)
				if firstString(definition, "name") == tool {
					schema, found = definition["inputSchema"]
					break
				}
			}
			if !found {
				return nil, b.fail("unknown_tool", fmt.Sprintf("%s has no caller-visible MCP tool %s", target, tool), nil)
			}
		}
		b.cachePreparedSchema(target, tool, schema)
	}
	if issue := validateMCPToolArguments(schema, schemaBusinessArguments(args)); issue != nil {
		status := http.StatusBadRequest
		if issue.Code == "invalid_tool_schema" {
			status = http.StatusBadGateway
		}
		return nil, b.fail(issue.Code, issue.Error(), map[string]any{
			"target": target, "tool": tool, "http_status": status,
			"validation": issue.metadata(), "dispatched": false,
		})
	}
	return this.Context().NewNull(), nil
}
