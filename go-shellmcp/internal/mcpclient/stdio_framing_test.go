package mcpclient

// Local JSON framing proofs only. No upstream integration claim.
import (
	"encoding/json"
	"fmt"
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
	input := strings.Repeat("{\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n", 4096)
	if _, err := readMatchingStdioResponse(json.NewDecoder(strings.NewReader(input)), 4); err == nil {
		t.Fatal("unbounded notifications accepted")
	}
}

// Regression: a valid reply after >64 asynchronous notifications is not a
// protocol error. The fixture is purely in-memory; no child or lease is called.
func TestStdioReplyAfterNotificationBurst(t *testing.T) {
	for _, count := range []int{65, 128, 1024, 4095} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			input := strings.Repeat("{\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n", count)
			input += "{\"jsonrpc\":\"2.0\",\"id\":4,\"result\":{}}\n"
			frame, err := readMatchingStdioResponse(json.NewDecoder(strings.NewReader(input)), 4)
			if err != nil || frame.Result == nil {
				t.Fatalf("valid reply after %d notifications: %v", count, err)
			}
		})
	}
}

func TestStdioBurstDoesNotHideProtocolErrors(t *testing.T) {
	prefix := strings.Repeat("{\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n", 128)
	cases := []struct{ name, frame, want string }{
		{"wrong_id", "{\"jsonrpc\":\"2.0\",\"id\":5,\"result\":{}}", "response ID does not match"},
		{"server_request", "{\"jsonrpc\":\"2.0\",\"id\":4,\"method\":\"sampling/createMessage\"}", "server requests are unsupported"},
		{"invalid_version", "{\"jsonrpc\":\"1.0\",\"id\":4,\"result\":{}}", "invalid JSON-RPC version"},
		{"missing_result", "{\"jsonrpc\":\"2.0\",\"id\":4}", "has no result or error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readMatchingStdioResponse(json.NewDecoder(strings.NewReader(prefix+tc.frame+"\n")), 4)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
}

func TestStdioBurstBoundary(t *testing.T) {
	notification := "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n"
	reply := "{\"jsonrpc\":\"2.0\",\"id\":4,\"result\":{}}\n"
	_, err := readMatchingStdioResponse(json.NewDecoder(strings.NewReader(strings.Repeat(notification, 4095)+reply)), 4)
	if err != nil {
		t.Fatalf("4096th frame must be accepted: %v", err)
	}
	_, err = readMatchingStdioResponse(json.NewDecoder(strings.NewReader(strings.Repeat(notification, 4096)+reply)), 4)
	if err == nil || !strings.Contains(err.Error(), "too many notifications") {
		t.Fatalf("4097th frame must be rejected: %v", err)
	}
}
