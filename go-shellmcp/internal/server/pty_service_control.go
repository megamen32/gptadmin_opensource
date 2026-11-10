package server

import (
	"path/filepath"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/shell"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/supervisor"
)

// Only terminal_spawn on the configured local PTY contract carries executable
// commands here. Terminal writes are opaque input, potentially to a remote
// program; this stateless guard does not interpret them or nested scripts.
func validatePTYServiceControl(agent supervisor.Agent, tool string, arguments map[string]any, validate func(shell.Request) error) error {
	if agent.Transport != "stdio" || agent.Ref != "PtyMcp" || agent.Name != "pty-mcp" || filepath.Base(agent.Command) != "pty-mcp" {
		return nil
	}
	var req shell.Request
	switch tool {
	case "terminal_spawn":
		executable, ok := arguments["shell"].(string)
		if !ok || executable == "" {
			return nil
		}
		args, ok := ptyServiceControlArgs(arguments["args"])
		if !ok {
			return nil
		}
		base := filepath.Base(executable)
		if (base == "bash" || base == "sh") && len(args) >= 2 && (args[0] == "-c" || args[0] == "-lc") {
			req.Cmd = args[1]
		} else {
			req.Argv = append([]string{executable}, args...)
		}
	default:
		return nil
	}
	return validate(req)
}

func ptyServiceControlArgs(raw any) ([]string, bool) {
	switch args := raw.(type) {
	case nil:
		return nil, true
	case []string:
		return args, true
	case []any:
		values := make([]string, len(args))
		for i, arg := range args {
			value, ok := arg.(string)
			if !ok {
				return nil, false
			}
			values[i] = value
		}
		return values, true
	default:
		return nil, false
	}
}
