package hub

import "strings"

func (b *codeModeBridge) recordValidation(target, tool string, issue *mcpArgumentError) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lastValidation, b.lastValidationTarget, b.lastValidationTool = issue, target, tool
}

// If an invalid input escapes the script, retain the structured field error.
// A caught validation error is not sticky and must not block a corrected call.
func (b *codeModeBridge) uncaughtValidation(evalErr error) *codeModeError {
	b.mu.Lock()
	issue, target, tool := b.lastValidation, b.lastValidationTarget, b.lastValidationTool
	b.mu.Unlock()
	if issue == nil || evalErr == nil || (!strings.Contains(evalErr.Error(), issue.Error()) && !strings.Contains(evalErr.Error(), issue.Message)) {
		return nil
	}
	return &codeModeError{code: "invalid_arguments", detail: issue.Error(), call: b.calls(), receipt: map[string]any{
		"target": target, "tool": tool, "validation": issue.metadata(), "dispatched": false,
	}}
}

// recoverableMCPValidationError is returned strictly before dispatch. Unlike a
// nonterminal MCP receipt, no operation was started or sent to a remote host.
// A Code Mode script may correct the arguments and try again in the same run.
type recoverableMCPValidationError struct {
	issue *mcpArgumentError
}

func (e *recoverableMCPValidationError) Error() string {
	if e == nil || e.issue == nil {
		return "invalid MCP input arguments"
	}
	return e.issue.Error()
}
