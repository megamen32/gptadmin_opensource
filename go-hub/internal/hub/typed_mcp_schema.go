package hub

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
)

const maxToolSchemaBytes = 128 << 10

// The compiler understands the full JSON Schema 2020-12 contract. Do not
// load remote references from tool descriptions: the source of truth is the
// MCP tool's own schema, not an arbitrary URL supplied by another server.
var mcpSchemaCache = struct {
	sync.Mutex
	items map[string]*jsonschema.Schema
}{items: make(map[string]*jsonschema.Schema)}

type mcpArgumentError struct {
	Code     string
	Path     string
	Keyword  string
	Expected any
	Actual   string
	Message  string
}

func (e *mcpArgumentError) Error() string {
	if e == nil {
		return ""
	}
	path := e.Path
	if path == "" {
		path = "/"
	}
	return fmt.Sprintf("%s at %s: %s", e.Code, path, e.Message)
}

func (e *mcpArgumentError) metadata() map[string]any {
	out := map[string]any{
		"code":    e.Code,
		"path":    e.Path,
		"keyword": e.Keyword,
		"message": e.Message,
	}
	if e.Expected != nil {
		out["expected"] = e.Expected
	}
	if e.Actual != "" {
		out["actual"] = e.Actual
	}
	return out
}

func compileMCPToolSchema(raw any) (*jsonschema.Schema, *mcpArgumentError) {
	bytesJSON, err := json.Marshal(raw)
	if err != nil || len(bytesJSON) == 0 || len(bytesJSON) > maxToolSchemaBytes {
		return nil, &mcpArgumentError{Code: "invalid_tool_schema", Message: "inputSchema is missing, too large or not JSON serializable"}
	}
	var parsed any
	if err := json.Unmarshal(bytesJSON, &parsed); err != nil || !localSchemaReferences(parsed) {
		return nil, &mcpArgumentError{Code: "invalid_tool_schema", Message: "inputSchema contains invalid or external JSON references"}
	}
	digest := sha256.Sum256(bytesJSON)
	key := hex.EncodeToString(digest[:])
	mcpSchemaCache.Lock()
	defer mcpSchemaCache.Unlock()
	if compiled := mcpSchemaCache.items[key]; compiled != nil {
		return compiled, nil
	}
	compiler := jsonschema.NewCompiler()
	compiler.Draft = jsonschema.Draft2020
	const schemaURI = "https://gptadmin.invalid/internal/mcp-input-schema.json"
	if err := compiler.AddResource(schemaURI, bytes.NewReader(bytesJSON)); err != nil {
		return nil, &mcpArgumentError{Code: "invalid_tool_schema", Message: "cannot parse inputSchema: " + err.Error()}
	}
	compiled, err := compiler.Compile(schemaURI)
	if err != nil {
		return nil, &mcpArgumentError{Code: "invalid_tool_schema", Message: "cannot compile inputSchema: " + err.Error()}
	}
	if len(mcpSchemaCache.items) >= 256 {
		mcpSchemaCache.items = make(map[string]*jsonschema.Schema)
	}
	mcpSchemaCache.items[key] = compiled
	return compiled, nil
}

func localSchemaReferences(node any) bool {
	switch item := node.(type) {
	case map[string]any:
		for key, value := range item {
			if key == "$ref" || key == "$dynamicRef" || key == "$recursiveRef" {
				ref, ok := value.(string)
				if !ok || !strings.HasPrefix(ref, "#") {
					return false
				}
			}
			if !localSchemaReferences(value) {
				return false
			}
		}
	case []any:
		for _, value := range item {
			if !localSchemaReferences(value) {
				return false
			}
		}
	}
	return true
}

func builtInMCPInputSchema(target, toolName string) (any, bool) {
	var definitions []map[string]any
	switch {
	case target == "hub":
		definitions = hubTools()
	case target == "control:hub":
		definitions = controlTools(target)
	case strings.HasPrefix(target, "shell:"):
		definitions = shellTools()
	case strings.HasPrefix(target, "file:"):
		definitions = fileTools()
	case strings.HasPrefix(target, "control:"):
		definitions = controlTools(target)
	default:
		return nil, false
	}
	for _, definition := range definitions {
		if firstString(definition, "name") == toolName {
			schema, exists := definition["inputSchema"]
			return schema, exists
		}
	}
	return nil, false
}

func schemaBusinessArguments(args map[string]any) map[string]any {
	clean := cloneMap(args)
	delete(clean, "approval_id")
	return clean
}

func validateMCPToolArguments(inputSchema any, args map[string]any) *mcpArgumentError {
	compiled, issue := compileMCPToolSchema(inputSchema)
	if issue != nil {
		return issue
	}
	// Marshal+decode ensures values conform to the JSON data model and
	// preserves integers that would otherwise lose precision as float64.
	encoded, err := json.Marshal(args)
	if err != nil {
		return &mcpArgumentError{Code: "invalid_arguments", Message: "arguments are not serializable JSON"}
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return &mcpArgumentError{Code: "invalid_arguments", Message: "arguments are not valid JSON"}
	}
	if err := compiled.Validate(value); err != nil {
		var ve *jsonschema.ValidationError
		if !errors.As(err, &ve) {
			return &mcpArgumentError{Code: "invalid_arguments", Message: err.Error()}
		}
		leaf := ve
		for len(leaf.Causes) > 0 {
			leaf = leaf.Causes[0]
		}
		keyword := leaf.KeywordLocation
		if slash := strings.LastIndexByte(keyword, '/'); slash >= 0 {
			keyword = keyword[slash+1:]
		}
		var schema any
		_ = json.Unmarshal(encodedSchemaJSON(inputSchema), &schema)
		expected := jsonPointerValue(schema, leaf.KeywordLocation)
		actual := jsonPointerValue(value, leaf.InstanceLocation)
		path := leaf.InstanceLocation
		if path == "" {
			path = "/"
		}
		message := leaf.Message
		if len(message) > 250 {
			message = message[:250]
		}
		actualType := jsonValueType(actual)
		if keyword == "required" {
			if required, ok := expected.([]any); ok {
				object := mapValue(jsonPointerValue(value, leaf.InstanceLocation))
				for _, raw := range required {
					name, ok := raw.(string)
					if !ok {
						continue
					}
					if _, present := object[name]; !present {
						path = strings.TrimSuffix(path, "/") + "/" + strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
						expected, actualType = "required", "missing"
						message = "required field " + path + " is missing"
						break
					}
				}
			}
		}
		if expected != nil && keyword == "type" {
			message = fmt.Sprintf("expected %v, got %s", expected, actualType)
		}
		return &mcpArgumentError{Code: "invalid_arguments", Path: path, Keyword: keyword, Expected: expected, Actual: actualType, Message: message}
	}
	return nil
}

func encodedSchemaJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}

func jsonPointerValue(root any, pointer string) any {
	pointer = strings.TrimPrefix(pointer, "#")
	if pointer == "" || pointer == "/" {
		return root
	}
	for _, segment := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		segment = strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
		switch value := root.(type) {
		case map[string]any:
			root = value[segment]
		case []any:
			i, err := strconv.Atoi(segment)
			if err != nil || i < 0 || i >= len(value) {
				return nil
			}
			root = value[i]
		default:
			return nil
		}
	}
	return root
}

func jsonValueType(v any) string {
	switch x := v.(type) {
	case nil:
		return "null or missing"
	case string:
		return "string"
	case bool:
		return "boolean"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case json.Number:
		if _, err := x.Int64(); err == nil {
			return "integer"
		}
		return "number"
	case float64, float32:
		return "number"
	case int, int64, int32:
		return "integer"
	default:
		return "unknown"
	}
}
