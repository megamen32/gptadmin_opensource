package shell

import (
	"context"
	"encoding/base64"
	"testing"
)

func TestStdoutBase64CarriesExactTailBytes(t *testing.T) {
	// CSI-style escape sequences must survive with a byte-exact base64 copy.
	res := Run(context.Background(), Request{Cmd: "printf 'a\033[mb\033[Hc'", SpillDir: t.TempDir()}, 8192)
	if res.ReturnCode != 0 {
		t.Fatalf("rc=%d", res.ReturnCode)
	}
	if res.Stdout != "a\x1b[mb\x1b[Hc" {
		t.Fatalf("stdout=%q", res.Stdout)
	}
	if res.StdoutBase64 == "" {
		t.Fatal("stdout_base64 missing for CSI output")
	}
	decoded, err := base64.StdEncoding.DecodeString(res.StdoutBase64)
	if err != nil || string(decoded) != res.Stdout {
		t.Fatalf("base64 mismatch: %v %q", err, decoded)
	}
}

func TestStdoutBase64OmittedForPlainOutput(t *testing.T) {
	res := Run(context.Background(), Request{Cmd: "echo plain", SpillDir: t.TempDir()}, 8192)
	if res.StdoutBase64 != "" {
		t.Fatalf("unexpected stdout_base64 for plain text: %q", res.StdoutBase64)
	}
}

func TestStdoutBase64CarriesInvalidUTF8Bytes(t *testing.T) {
	// Invalid UTF-8 is replaced by U+FFFD in the JSON string form; the base64
	// copy must keep the original bytes.
	res := Run(context.Background(), Request{Cmd: "printf 'ab\\377cd'", SpillDir: t.TempDir()}, 8192)
	if res.ReturnCode != 0 {
		t.Fatalf("rc=%d", res.ReturnCode)
	}
	decoded, err := base64.StdEncoding.DecodeString(res.StdoutBase64)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{'a', 'b', 0xff, 'c', 'd'}
	if string(decoded) != string(want) {
		t.Fatalf("decoded=%q want=%q", decoded, want)
	}
}
