package hub

import "testing"

func TestHubShellExecSchemaAdvertisesNativeArgvAsAlternative(t *testing.T) {
	tools := shellTools()
	for _, tool := range tools {
		if firstString(tool, "name") != "shell_exec" {
			continue
		}
		schema := mapValue(tool["inputSchema"])
		props := mapValue(schema["properties"])
		if mapValue(props["argv"])["minItems"] != 1 || mapValue(props["argv"])["maxItems"] != 256 {
			t.Fatalf("hub shell schema is missing native argv bounds: %#v", schema)
		}
		if len(outputRows(schema["oneOf"])) != 3 {
			t.Fatalf("hub shell schema must require either cmd or argv: %#v", schema)
		}
		if _, exists := props["cmd"]; !exists {
			t.Fatal("hub shell schema broke existing cmd syntax")
		}
		return
	}
	t.Fatal("hub shell_exec tool missing")
}
