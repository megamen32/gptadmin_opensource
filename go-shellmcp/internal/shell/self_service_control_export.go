package shell

// CheckSelfServiceControl uses this process's cgroup to check another local
// execution entry point, without executing the request or invoking systemctl.
func CheckSelfServiceControl(req Request) error { return checkSelfServiceControl(req) }

// ValidateSelfServiceControl is the same literal-command policy with explicit
// cgroup input, for adapters and deterministic unit fixtures.
func ValidateSelfServiceControl(cgroup string, req Request) error {
	return selfServiceControlError(cgroup, req)
}
