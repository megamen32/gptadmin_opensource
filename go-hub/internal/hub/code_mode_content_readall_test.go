package hub

import (
	"fmt"
	"net/http"
	"testing"
)

func TestContentReadAllPreservesUnicodeAndExactBytes(t *testing.T) {
	s, r := codeModeFixture(t)
	defer s.Close()
	code := `
 const text="Line one with apostrophe and Unicode 🌍\\n".repeat(2800)+"конец 🌍\\n";
 const source=await mcp.content.index(text,{name:"readall-test"});
 const copy=await mcp.content.readAll(source.source,{max_bytes:150000});
 return {same:copy.text===text,complete:copy.complete,bytes:copy.bytes,chunks:copy.chunks,format:copy.format};
 `
	out, status := codeModeRun(t, s, r, map[string]any{"code": code, "timeout_ms": 12000})
	got := mapValue(out["result"])
	if status != http.StatusOK || got["same"] != true || got["complete"] != true || intFromAny(got["chunks"]) < 2 {
		t.Fatalf("content chunks were not faithfully reconstructed: status=%d %#v", status, out)
	}
}

func TestContentReadAllRequiresOriginAuthorizationAndExplicitSize(t *testing.T) {
	s, r := codeModeFixture(t)
	defer s.Close()
	indexed, status := codeModeRun(t, s, r, map[string]any{"code": `return await mcp.content.index("short source");`})
	if status != http.StatusOK {
		t.Fatalf("index failed: %#v", indexed)
	}
	source := firstString(mapValue(indexed["result"]), "source")
	denied := requestWithAuthClaims(r, map[string]any{"sub": "other", "client_id": "other", "scope": "gptadmin.read gptadmin.exec", "access_mode": accessModeFull})
	out, status := codeModeRun(t, s, denied, map[string]any{"code": fmt.Sprintf("return await mcp.content.readAll(%q);", source)})
	if status == http.StatusOK || firstString(mapValue(out["error"]), "code") != "history_unavailable" {
		t.Fatalf("source was readable by another principal: %d %#v", status, out)
	}
	out, status = codeModeRun(t, s, r, map[string]any{"code": fmt.Sprintf("return await mcp.content.readAll(%q,{max_bytes:5});", source)})
	if status == http.StatusOK || firstString(mapValue(out["error"]), "code") != "output_too_large" {
		t.Fatalf("bounded read accepted source exceeding max_bytes: %d %#v", status, out)
	}
}

func TestContentReadAllMarksPartialSource(t *testing.T) {
	s, r := codeModeFixture(t)
	defer s.Close()
	store, err := s.contentStoreForExec()
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.SaveIncompleteFormatted(operationPrincipal(r), "partial", contentFreeTextOrigin, "text", "incomplete-data")
	if err != nil {
		t.Fatal(err)
	}
	out, status := codeModeRun(t, s, r, map[string]any{"code": fmt.Sprintf("return await mcp.content.readAll(%q);", source.ID)})
	if status == http.StatusOK || firstString(mapValue(out["error"]), "code") != "content_incomplete" {
		t.Fatalf("partial content was mislabeled complete: %d %#v", status, out)
	}
	out, status = codeModeRun(t, s, r, map[string]any{"code": fmt.Sprintf("return await mcp.content.readAll(%q,{allow_partial:true});", source.ID)})
	if status != http.StatusOK || mapValue(out["result"])["complete"] != false || firstString(mapValue(out["result"]), "text") != "incomplete-data" {
		t.Fatalf("explicit partial read not marked: %d %#v", status, out)
	}
}

// Exercise the exact object argument advertised by an oversized exec response,
// rather than manually translating it to the positional API.
func TestContentReadAllAcceptsAdvertisedDescriptor(t *testing.T) {
	s, r := codeModeFixture(t)
	defer s.Close()
	saved, status := codeModeRun(t, s, r, map[string]any{"code": "return 'xyz'.repeat(4000);", "max_output_bytes": 4096})
	if status != http.StatusOK {
		t.Fatalf("save: %d %#v", status, saved)
	}
	descriptor := mapValue(mapValue(saved["result"])["read_all"])
	args := mapValue(descriptor["args"])
	if firstString(descriptor, "call") != "mcp.content.readAll" || firstString(args, "source") == "" {
		t.Fatalf("missing readAll descriptor: %#v", descriptor)
	}
	code := fmt.Sprintf("const x=await mcp.content.readAll({source:%q,max_bytes:%d}); return {same:x.text==='xyz'.repeat(4000),bytes:x.bytes,complete:x.complete};", firstString(args, "source"), intFromAny(args["max_bytes"]))
	out, status := codeModeRun(t, s, r, map[string]any{"code": code})
	got := mapValue(out["result"])
	if status != http.StatusOK || got["same"] != true || got["complete"] != true || intFromAny(got["bytes"]) != 12000 {
		t.Fatalf("advertised readAll call failed: %d %#v", status, out)
	}
}
