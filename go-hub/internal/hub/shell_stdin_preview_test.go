package hub

import (
	"strings"
	"testing"
)

func TestShellArgumentsPreviewHidesScriptSourceButNotCommand(t *testing.T) {
	private := "source with \"quotes\" and $HOME; an access key that must not be displayed"
	args := map[string]any{"argv": []any{"python3", "-"}, "stdin": private, "cwd": "/srv/app"}
	preview := shellArgumentsPreview(args, nil)
	if strings.Contains(preview, private) || strings.Contains(preview, "access key") {
		t.Fatalf("script source shown in preview: %q", preview)
	}
	if !strings.Contains(preview, "stdin source omitted") || !strings.Contains(preview, "python3") {
		t.Fatalf("no useful command metadata: %q", preview)
	}
	if args["stdin"] != private {
		t.Fatal("preview mutated real task arguments")
	}
}
