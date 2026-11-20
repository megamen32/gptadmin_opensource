//go:build !linux

package server

func currentUpdateLaunch() (updateLaunch, error) {
	return directUpdateLaunch()
}
