//go:build !linux

package node

import "context"

// macOS launchd and other native managers retain their own lifecycle.
func startNativeLifecycleReconcile(context.Context) func() { return func() {} }
