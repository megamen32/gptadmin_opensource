package mcpclient

// Local JSON framing proofs only. No upstream integration claim.
import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStdioReplySkipsNotificationsAndMatchesRequest(t *testing.T) {
	dec := json.NewDecoder(strings.NewReader("{\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n{\"jsonrpc\":\"2.0\",\"id\":4,\"result\":{\"tools\":[]}}\n"))
	frame, err := readMatchingStdioResponse(dec, 4)
	if err != nil || frame.Result == nil {
		t.Fatalf("notification contaminated reply: %v", err)
	}
}

func TestStdioReplyRejectsWrongIDAndServerRequest(t *testing.T) {
	for _, input := range []string{
		`{"jsonrpc":"2.0","id":5,"result":{}}`,
		`{"jsonrpc":"2.0","id":4,"method":"sampling/createMessage","params":{}}`,
		`{"jsonrpc":"2.0","result":{}}`,
	} {
		if _, err := readMatchingStdioResponse(json.NewDecoder(strings.NewReader(input)), 4); err == nil {
			t.Fatal("unexpected upstream frame accepted")
		}
	}
}

func TestStdioReplyBoundsNotificationFrames(t *testing.T) {
	input := strings.Repeat("{\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n", 64)
	if _, err := readMatchingStdioResponse(json.NewDecoder(strings.NewReader(input)), 4); err == nil {
		t.Fatal("unbounded notifications accepted")
	}
}
