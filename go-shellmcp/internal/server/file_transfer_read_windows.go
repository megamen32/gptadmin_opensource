//go:build windows

package server

import "os"

func transferOpenRead(root *os.Root, path string) (*os.File, error) { return root.Open(path) }
