//go:build !unix

package node

import "os"

// A Unix socket diagnostic endpoint must not proceed without a verifiable Unix
// owner. Windows builds compile normally; opt-in diagnostics fail closed until
// a platform-specific ownership and private-access check is implemented.
func diagnosticsOwnedByCurrentUser(os.FileInfo) bool {
	return false
}
