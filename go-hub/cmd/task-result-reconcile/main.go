// task-result-reconcile repairs one reviewed, already executed missing receipt.
// It neither executes the task nor acknowledges/removes its executor outbox.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/megamen32/gptadmin/go-hub/internal/hub"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "task result reconciliation:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("action required: origin, prepare, apply, inspect")
	}
	action := args[0]
	flags := flag.NewFlagSet("task-result-reconcile "+action, flag.ContinueOnError)
	config := flags.String("config-dir", "", "must match actual service environment")
	quorumConfig := flags.String("quorum-config", "", "existing local voter configuration for read-only origin export")
	job := flags.String("job-id", "", "exact original job ID")
	output := flags.String("output", "", "new private evidence file or plan directory")
	target := flags.String("target", "", "exact physical shell target")
	localID := flags.String("expect-local-id", "", "expected physical executor identity")
	physical := flags.String("physical-identity", "", "explicit configured shellmcp_identity.json")
	pid := flags.Int("runtime-pid", 0, "reviewed live ordinary-mode Node PID")
	executable := flags.String("runtime-exe-sha256", "", "reviewed exact live Node binary SHA256")
	localQuorum := flags.String("local-quorum-config", "", "existing recipient voter configuration, read-only")
	source := flags.String("source-evidence", "", "private independent source-voter evidence")
	sourceSHA := flags.String("source-evidence-sha256", "", "reviewed exact source-voter evidence SHA256")
	outbox := flags.String("outbox", "", "exact existing executor outbox entry")
	prepared := flags.String("prepared", "", "private prepared repair directory")
	planSHA := flags.String("plan-sha256", "", "reviewed exact prepared plan SHA256")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	var value any
	var err error
	if action == "origin" {
		value, err = hub.ExportTerminalOrigin(*quorumConfig, *job, *output)
	} else {
		cfg := hub.FromEnv()
		requested, e := filepath.Abs(*config)
		if e != nil || *config == "" || requested != cfg.ConfigDir {
			return errors.New("explicit config directory differs from effective service environment")
		}
		switch action {
		case "prepare":
			value, err = hub.PrepareTerminalResult(cfg, hub.TerminalResultPrepareOptions{Target: *target, ExpectedLocalID: *localID, PhysicalIdentityFile: *physical, RuntimePID: *pid, RuntimeExecutableSHA256: *executable, LocalQuorumConfig: *localQuorum, SourceEvidence: *source, SourceEvidenceSHA256: *sourceSHA, OutboxFile: *outbox, OutputDir: *output})
		case "apply":
			value, err = hub.ApplyTerminalResult(cfg, *prepared, *planSHA)
		case "inspect":
			value, err = hub.InspectTerminalResult(cfg, *prepared, *planSHA)
		default:
			return errors.New("unknown action")
		}
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(value)
}
