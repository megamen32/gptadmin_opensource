package hub

import (
	"strings"
	"testing"
)

func TestTypedMCPValidationRequiredTypeRangeAndAdditionalProperties(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "minLength": 2},
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 5},
		},
		"required":             []string{"query"},
		"additionalProperties": false,
	}
	for _, args := range []map[string]any{
		{"query": "a valid request", "limit": 5},
		{"query": "заказ", "limit": int64(2)},
	} {
		if issue := validateMCPToolArguments(schema, args); issue != nil {
			t.Fatalf("valid MCP JSON arguments rejected: %v", issue)
		}
	}
	cases := []struct {
		args    map[string]any
		keyword string
		path    string
	}{
		{map[string]any{"limit": 4}, "required", "/query"},
		{map[string]any{"query": "ok", "limit": "five"}, "type", "/limit"},
		{map[string]any{"query": "ok", "limit": 8}, "maximum", "/limit"},
		{map[string]any{"query": "ok", "unused": true}, "additionalProperties", "/"},
		{map[string]any{"query": "x"}, "minLength", "/query"},
	}
	for _, test := range cases {
		issue := validateMCPToolArguments(schema, test.args)
		if issue == nil || issue.Code != "invalid_arguments" || issue.Keyword != test.keyword || issue.Path != test.path {
			t.Errorf("invalid args %+v: issue=%+v; want keyword=%s path=%s", test.args, issue, test.keyword, test.path)
		}
	}
}

func TestTypedMCPValidationOneOfEnumAndNestedItemTypes(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"oneOf": []any{
			map[string]any{"required": []string{"cmd"}},
			map[string]any{"required": []string{"argv"}},
		},
		"properties": map[string]any{
			"cmd":     map[string]any{"type": "string"},
			"argv":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1},
			"options": map[string]any{"type": "object", "properties": map[string]any{"mode": map[string]any{"enum": []string{"read", "write"}}}},
		},
	}
	for _, args := range []map[string]any{
		{"cmd": "printf legacy"},
		{"argv": []any{"printf", "%s", "it's quoted 🌍"}},
		{"argv": []any{"echo", "hi"}, "options": map[string]any{"mode": "read"}},
	} {
		if issue := validateMCPToolArguments(schema, args); issue != nil {
			t.Fatalf("valid args rejected: %v", issue)
		}
	}
	for _, args := range []map[string]any{
		{},
		{"cmd": "id", "argv": []any{"id"}},
		{"argv": []any{"echo", 5}},
		{"cmd": "true", "options": map[string]any{"mode": "destroy"}},
	} {
		if issue := validateMCPToolArguments(schema, args); issue == nil {
			t.Errorf("invalid input passed schema: %#v", args)
		}
	}
}

func TestTypedMCPSchemaDisallowsExternalReferences(t *testing.T) {
	remote := map[string]any{"$ref": "https://example.org/malicious-or-outdated-schema.json"}
	issue := validateMCPToolArguments(remote, map[string]any{"a": 1})
	if issue == nil || issue.Code != "invalid_tool_schema" {
		t.Fatalf("external schema reference was allowed: %#v", issue)
	}
	local := map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"$ref": "#/$defs/id"}}, "$defs": map[string]any{"id": map[string]any{"type": "integer"}}}
	if issue := validateMCPToolArguments(local, map[string]any{"id": 4}); issue != nil {
		t.Fatalf("valid local reference rejected: %v", issue)
	}
}

func TestTypedMCPErrorNeverEchoesArgumentValues(t *testing.T) {
	secret := "sensitive-user-provided-secret-12345678"
	issue := validateMCPToolArguments(map[string]any{"type": "object", "properties": map[string]any{"timeout": map[string]any{"type": "integer"}}}, map[string]any{"timeout": secret})
	if issue == nil || issue.Path != "/timeout" || issue.Actual != "string" || issue.Expected != "integer" {
		t.Fatalf("typed error is not actionable: %#v", issue)
	}
	if strings.Contains(issue.Error(), secret) || strings.Contains(issue.Message, secret) {
		t.Fatalf("argument value leaked via validation: %#v", issue)
	}
}
