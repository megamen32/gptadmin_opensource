//go:build !linux

package shell

func checkSelfServiceControl(req Request) error { return nil }
