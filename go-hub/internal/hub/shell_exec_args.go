package hub

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// validateShellExecArguments protects the queue before a task is persisted.
// cmd retains the legacy shell command contract. argv always means direct
// process execution and is never converted to a shell command string.
func validateShellExecArguments(args map[string]any) error {
	if raw, present := args["stdin"]; present {
		source, ok := raw.(string)
		if !ok || len(source) > 256<<10 || strings.ContainsRune(source, '\x00') || !utf8.ValidString(source) {
			return errors.New("shell_exec stdin must be valid UTF-8 without NUL, up to 262144 bytes")
		}
	}
	raw, hasArgv := args["argv"]
	_, hasCmd := args["cmd"]
	_, hasCommand := args["command"]
	if hasArgv {
		if hasCmd || hasCommand {
			return errors.New("shell_exec accepts cmd or argv, not both")
		}
		items := []string{}
		switch values := raw.(type) {
		case []string:
			items = values
		case []any:
			for _, item := range values {
				value, ok := item.(string)
				if !ok {
					return errors.New("shell_exec argv entries must be strings")
				}
				items = append(items, value)
			}
		default:
			return errors.New("shell_exec argv must be an array")
		}
		if len(items) == 0 || len(items) > 256 {
			return errors.New("shell_exec argv must have 1..256 entries")
		}
		bytes := 0
		for i, item := range items {
			if strings.ContainsRune(item, '\x00') || !utf8.ValidString(item) {
				return fmt.Errorf("shell_exec argv[%d] contains NUL or invalid UTF-8", i)
			}
			if i == 0 && strings.TrimSpace(item) == "" {
				return errors.New("shell_exec argv[0] must identify an executable")
			}
			bytes += len(item)
			if bytes > 65536 {
				return errors.New("shell_exec argv exceeds 65536 bytes")
			}
		}
		return nil
	}
	if strings.TrimSpace(firstString(args, "cmd", "command")) == "" {
		return errors.New("missing cmd or argv")
	}
	return nil
}
