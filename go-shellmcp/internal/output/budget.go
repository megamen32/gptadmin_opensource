package output

const (
	// DefaultInlineTailBytes is the per-agent ShellMCP inline stdout/stderr budget.
	// Larger command output is still spooled to disk and referenced by path.
	DefaultInlineTailBytes int64 = 64 * 1024
)
