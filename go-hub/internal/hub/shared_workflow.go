package hub

import (
	_ "embed"
	"fmt"
	"strings"
	"unicode/utf8"
)

// One shipped component for every profile. Persisted profile documents remain
// owner-managed and byte-for-byte unchanged; composition occurs only on delivery.
//
//go:embed shared_workflow.md
var sharedWorkflowInstructions string

const sharedWorkflowMaxBytes = 4 * 1024
const effectiveInstructionsMaxBytes = startupInstructionsMaxBytes + sharedWorkflowMaxBytes + 128

func composeSharedWorkflow(set InstructionSet, workflow string) (InstructionSet, error) {
	if err := validateInstructionContent(set.Content); err != nil {
		return InstructionSet{}, err
	}
	if strings.TrimSpace(workflow) == "" || !utf8.ValidString(workflow) || len(workflow) > sharedWorkflowMaxBytes {
		return InstructionSet{}, fmt.Errorf("shared workflow is empty, invalid or exceeds %d bytes", sharedWorkflowMaxBytes)
	}
	// Never rewrite or truncate the profile, including its whitespace.
	set.Content += "\n\n<!-- gptadmin-shared-workflow -->\n" + workflow
	if len(set.Content) > effectiveInstructionsMaxBytes {
		return InstructionSet{}, fmt.Errorf("effective instructions exceed %d bytes", effectiveInstructionsMaxBytes)
	}
	set.Version = instructionSetVersion(set.Content)
	return set, nil
}
