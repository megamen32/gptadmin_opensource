package hub

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fastschema/qjs"
)

// The test strings below are assembled from safe pieces (no literal CSI-like
// runs appear in this source file), because the very bug under test eats such
// runs from anything crossing the sandbox boundary.

// jsCSI builds a JS expression whose value is text with CSI-like runs.
func jsCSI() string {
	// 'x' + ESC-bracket runs through String.fromCharCode so this file itself
	// never contains a raw "[" + "m" sequence.
	return `'x'+String.fromCharCode(91)+'m y'+String.fromCharCode(91)+'h z'+String.fromCharCode(91)+` +
		`'c w'+String.fromCharCode(91)+'38;5;196m e'+String.fromCharCode(91)+'?25l'`
}

// wantCSI is the expected byte-exact text of jsCSI.
func wantCSI() string {
	br := string(rune(91))
	return "x" + br + "m y" + br + "h z" + br + "c w" + br + "38;5;196m e" + br + "?25l"
}

// TestCodeModeStringHexKeepsCSI: the stock qjs.wasm of fastschema/qjs v0.0.6
// strips CSI-like byte runs from strings crossing JS_ToCString and from
// ArrayBuffer bytes read through QJS_GetArrayBuffer. The hex bridge installed
// by codeModeAPISource must keep every such value byte-exact, including real
// escape sequences, OSC payloads, private-mode sequences and non-ASCII text.
func TestCodeModeStringHexKeepsCSI(t *testing.T) {
	rt, err := qjs.New()
	if err != nil {
		t.Skipf("qjs runtime unavailable: %v", err)
	}
	defer rt.Close()
	ctx := rt.Context()
	if _, err := ctx.Eval("code_mode_api.js", qjs.Code(codeModeAPISource)); err != nil {
		t.Fatalf("cannot expose the code_mode API: %v", err)
	}

	// 1) hexify on the raw string value.
	v, err := ctx.Eval("case.js", qjs.Code(jsCSI()))
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	got, ok := codeModeStringHex(ctx, v, "hexify")
	v.Free()
	if !ok {
		t.Fatal("hex bridge unavailable")
	}
	// hexify сериализует значение как JSON: строка оборачивается в кавычки.
	if got != `"`+wantCSI()+`"` {
		t.Fatalf("hexify not exact:\n got %q\nwant %q", got, `"`+wantCSI()+`"`)
	}

	// 2) hexify on an object value.
	code := "({s:" + jsCSI() + "})"
	v2, err := ctx.Eval("case2.js", qjs.Code(code))
	if err != nil {
		t.Fatalf("eval2: %v", err)
	}
	got2, ok := codeModeStringHex(ctx, v2, "hexify")
	v2.Free()
	if !ok {
		t.Fatal("hex bridge unavailable (2)")
	}
	br := string(rune(91))
	want2 := `{"s":"x` + br + `m y` + br + `h z` + br + `c w` + br + `38;5;196m e` + br + `?25l"}`
	if got2 != want2 {
		t.Fatalf("hexify JSON not exact:\n got %q\nwant %q", got2, want2)
	}

	// 3b) module namespace (как evalCodeMode получает результат) проходит мост.
	modCode := "export default {value: await (async () => ({selfTest:" + jsCSI() + ", n: 5}))()};"
	v3b, err := ctx.Eval("code_mode.js", qjs.Code(modCode), qjs.TypeModule(), qjs.FlagAsync())
	if err != nil {
		t.Fatalf("eval module: %v", err)
	}
	textM, err := stringifyCodeModeValue(v3b)
	v3b.Free()
	if err != nil {
		t.Fatalf("stringify module: %v", err)
	}
	br2 := string(rune(91))
	wantM := `{"value":{"selfTest":"x` + br2 + `m y` + br2 + `h z` + br2 + `c w` + br2 + `38;5;196m e` + br2 + `?25l","n":5}}`
	if textM != wantM {
		t.Fatalf("module namespace not exact:\n got %q\nwant %q", textM, wantM)
	}

	// 2b) mcp.text.hex — публичный hex-транспорт для агентов.
	code2b := "globalThis.mcp.text.hex(" + jsCSI() + ")"
	v2b, err := ctx.Eval("t.js", qjs.Code(code2b))
	if err != nil {
		t.Fatalf("eval text.hex: %v", err)
	}
	hexText := v2b.String()
	v2b.Free()
	raw2b, err := hex.DecodeString(hexText)
	if err != nil {
		t.Fatalf("text.hex payload invalid: %v", err)
	}
	if string(raw2b) != wantCSI() {
		t.Fatalf("mcp.text.hex not exact:\n got %q\nwant %q", string(raw2b), wantCSI())
	}

	// 3) objects with real escape sequences survive stringifyCodeModeValue.
	escCode := "'A'+String.fromCharCode(27)+'[31mB'+String.fromCharCode(27)+'[0mC'+String.fromCharCode(27)+']0;t'"
	obj := "({v:" + escCode + ", n: 5})"
	v3, err := ctx.Eval("case3.js", qjs.Code(obj))
	if err != nil {
		t.Fatalf("eval3: %v", err)
	}
	text, err := stringifyCodeModeValue(v3)
	v3.Free()
	if err != nil {
		t.Fatalf("stringify: %v", err)
	}
	want3 := `{"v":"A` + `\u001b[31mB\u001b[0mC\u001b]0;t","n":5}`
	if text != want3 {
		t.Fatalf("stringifyCodeModeValue not exact:\n got %q\nwant %q", text, want3)
	}
}

// TestCodeModeLogPayloadHex verifies the console.log hex transport marker.
func TestCodeModeLogPayloadHex(t *testing.T) {
	rt, err := qjs.New()
	if err != nil {
		t.Skipf("qjs runtime unavailable: %v", err)
	}
	defer rt.Close()
	ctx := rt.Context()
	if _, err := ctx.Eval("code_mode_api.js", qjs.Code(codeModeAPISource)); err != nil {
		t.Fatalf("cannot expose the code_mode API: %v", err)
	}
	// Прелюдия консоли: 'xh:' + hex(json)
	code := "'xh:'+globalThis.__codeModeStrBytes.hex(" + jsCSI() + ")"
	v, err := ctx.Eval("logcase.js", qjs.Code(code))
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	decoded := codeModeLogPayload(v)
	v.Free()
	if decoded != wantCSI() {
		t.Fatalf("log payload not exact:\n got %q\nwant %q", decoded, wantCSI())
	}
	// Немаркированная строка проходит как есть.
	v2, err := ctx.Eval("plain.js", qjs.Code("'plain text'"))
	if err != nil {
		t.Fatalf("eval plain: %v", err)
	}
	if got := codeModeLogPayload(v2); got != "plain text" {
		t.Fatalf("plain passthrough broken: %q", got)
	}
	v2.Free()
}

// Serialization must execute getters and toJSON exactly once, including when
// the bridge uses its plain-string fast path or the first invocation throws.
func TestCodeModeStringSerializationRunsOnce(t *testing.T) {
	for _, throws := range []bool{false, true} {
		name := "ordinary"
		if throws {
			name = "throwing"
		}
		t.Run(name, func(t *testing.T) {
			rt, err := qjs.New()
			if err != nil {
				t.Fatal(err)
			}
			defer rt.Close()
			ctx := rt.Context()
			api, err := ctx.Eval("serializer_api.js", qjs.Code(codeModeAPISource))
			if err != nil {
				t.Fatal(err)
			}
			api.Free()
			body := "globalThis.serializationCalls=(globalThis.serializationCalls||0)+1;"
			if throws {
				body += "if(globalThis.serializationCalls===1)throw Error('first serialization failed');"
			}
			body += "return {calls:globalThis.serializationCalls};"
			value, err := ctx.Eval("serializer_value.js", qjs.Code("({toJSON(){"+body+"}})"))
			if err != nil {
				t.Fatal(err)
			}
			encoded, encodeErr := stringifyCodeModeValue(value)
			value.Free()
			calls := ctx.Global().GetPropertyStr("serializationCalls")
			count := calls.Int64()
			calls.Free()
			if count != 1 {
				t.Errorf("toJSON executed %d times, want exactly one", count)
			}
			if throws {
				if encodeErr == nil {
					t.Errorf("initial toJSON error was swallowed: %q", encoded)
				}
			} else if encodeErr != nil || encoded != `{"calls":1}` {
				t.Errorf("serialization changed: got %q, err %v", encoded, encodeErr)
			}
		})
	}
}

func TestCodeModeStringSerializationUndefined(t *testing.T) {
	rt, err := qjs.New()
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	ctx := rt.Context()
	api, err := ctx.Eval("undefined_api.js", qjs.Code(codeModeAPISource))
	if err != nil {
		t.Fatal(err)
	}
	api.Free()
	value, err := ctx.Eval("undefined_value.js", qjs.Code("undefined"))
	if err != nil {
		t.Fatal(err)
	}
	defer value.Free()
	encoded, err := stringifyCodeModeValue(value)
	if err != nil || encoded != "undefined" {
		t.Fatalf("undefined changed: got %q, err %v", encoded, err)
	}
}

// Large serialized strings must preserve both CSI-like text and multibyte
// UTF-8 under the production default heap budget.
func TestCodeModeStringBridgeAboveOneMiBPreservesBytes(t *testing.T) {
	rt, err := qjs.New(qjs.Option{MemoryLimit: codeModeDefaultHeapBytes})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	ctx := rt.Context()
	api, err := ctx.Eval("large_api.js", qjs.Code(codeModeAPISource))
	if err != nil {
		t.Fatal(err)
	}
	api.Free()
	code := "'a'.repeat(1048593)+'x'+String.fromCharCode(91)+'m y'+String.fromCharCode(91)+'h '+String.fromCharCode(0x43c,0x438,0x440)+' '+String.fromCodePoint(0x1f30d)"
	value, err := ctx.Eval("large_value.js", qjs.Code(code))
	if err != nil {
		t.Fatal(err)
	}
	defer value.Free()
	encoded, err := stringifyCodeModeValue(value)
	if err != nil {
		t.Fatal(err)
	}
	var got string
	if err := json.Unmarshal([]byte(encoded), &got); err != nil {
		t.Fatal(err)
	}
	bracket := string(rune(91))
	want := strings.Repeat("a", 1048593) + "x" + bracket + "m y" + bracket + "h мир 🌍"
	if got != want {
		t.Fatalf("large fallback changed bytes: got %d bytes, want %d", len(got), len(want))
	}
	t.Logf("large fallback preserved all %d UTF-8 bytes", len(got))
}
