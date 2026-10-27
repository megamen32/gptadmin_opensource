package hub

import (
	"net/http/httptest"
	"os"
	"testing"
)

func TestInstructionSessionReceiptsEnabledByDefault(t *testing.T) {
	t.Setenv("GPTADMIN_INSTRUCTION_SESSION_RECEIPTS", "")
	if !FromEnv().InstructionSessionReceipts {
		t.Fatal("instruction session receipts must be enabled by default to avoid re-sending profile instructions")
	}
}

func TestInstructionReceiptSuppressesRepeatAndTokenEcho(t *testing.T) {
	s, r, _ := instructionFixture(t)
	s.cfg.InstructionSessionReceipts = true
	first := instructionDiscovery(t, s, r)
	if first["instructions"] == nil {
		t.Fatal("first delivery must carry the instructions")
	}
	again := mapValue(s.appsSDKCallForRequest(r, "search", map[string]any{"query": "status"}))
	if again["instructions"] != nil || again["context"] != nil {
		t.Fatalf("instructions repeated for a connection that already received them: %v", again)
	}
	out := mapValue(s.appsSDKCallForRequest(r, "exec", map[string]any{"code": "return {answer:42};"}))
	if out["executed"] == false || out["error"] != nil {
		t.Fatalf("exec without a context token was refused after delivery: %v", out)
	}
}

func TestInstructionReceiptFromRefusalAllowsRetryWithoutToken(t *testing.T) {
	s, r, _ := instructionFixture(t)
	s.cfg.InstructionSessionReceipts = true
	blocked := mapValue(s.appsSDKCallForRequest(r, "exec", map[string]any{"code": "return {answer:42};"}))
	if blocked["executed"] != false || blocked["instructions"] == nil {
		t.Fatalf("first exec must be refused with the instructions: %v", blocked)
	}
	out := mapValue(s.appsSDKCallForRequest(r, "exec", map[string]any{"code": "return {answer:42};"}))
	if out["executed"] == false || out["error"] != nil {
		t.Fatalf("retry without a token was refused: %v", out)
	}
}

func TestInstructionReceiptProtocolDeliveryCounts(t *testing.T) {
	s, r, _ := instructionFixture(t)
	s.cfg.InstructionSessionReceipts = true
	if s.validInstructionContext(r) {
		t.Fatal("no delivery yet")
	}
	s.noteInstructionsDelivered(r)
	if !s.validInstructionContext(r) {
		t.Fatal("server/discover or initialize delivery was not recorded")
	}
}

func TestInstructionReceiptIsPerCredential(t *testing.T) {
	s, r, _ := instructionFixture(t)
	s.cfg.InstructionSessionReceipts = true
	s.noteInstructionsDelivered(r)
	other := httptest.NewRequest("POST", "/mcp", nil)
	other.Header.Set("Authorization", "Bearer someone-else")
	if s.hasInstructionReceipt(other) {
		t.Fatal("receipt leaked to another credential")
	}
	anonymous := httptest.NewRequest("POST", "/mcp", nil)
	s.noteInstructionsDelivered(anonymous)
	if s.hasInstructionReceipt(anonymous) {
		t.Fatal("unauthenticated callers must never hold a receipt")
	}
}

func TestInstructionReceiptInvalidatedByChangedDocument(t *testing.T) {
	s, r, path := instructionFixture(t)
	s.cfg.InstructionSessionReceipts = true
	s.noteInstructionsDelivered(r)
	if !s.hasInstructionReceipt(r) {
		t.Fatal("receipt missing")
	}
	if err := os.WriteFile(path, []byte("Fixture instructions version two."), 0600); err != nil {
		t.Fatal(err)
	}
	if s.hasInstructionReceipt(r) {
		t.Fatal("receipt survived a change of the instruction document")
	}
}

func TestInstructionReceiptDisabledKeepsPerCallReceipts(t *testing.T) {
	s, r, _ := instructionFixture(t)
	s.cfg.InstructionSessionReceipts = false
	s.noteInstructionsDelivered(r)
	if s.hasInstructionReceipt(r) {
		t.Fatal("receipts must be off when the setting is off")
	}
}
