//go:build windows

package storagebudget

// FilesystemLimit keeps the absolute 500 MiB safety cap on Windows. Linux and
// macOS additionally derive the lower 5% bound from statfs, which are the
// constrained deployment targets covered by the ShellMCP host contract.
func FilesystemLimit(string) (int64, error) { return maxBudgetBytes, nil }
