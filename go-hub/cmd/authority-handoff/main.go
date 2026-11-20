// authority-handoff is an offline, exact-digest role transition tool. It must
// run from a supervisor outside the stopped GPTAdmin service's cgroup, with
// that service's effective configuration environment. It never stops a host,
// bypasses a lifecycle lock, clears a queue, or starts a runtime.
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
		fmt.Fprintln(os.Stderr, "authority handoff:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("action required: preflight, export, prepare-writer, prepare-reader, apply, inspect, authorize-activation, restore-before-activation")
	}
	action := args[0]
	flags := flag.NewFlagSet("authority-handoff "+action, flag.ContinueOnError)
	configDir := flags.String("config-dir", "", "must match the actual service environment")
	target := flags.String("target", "", "exact shell target")
	writerID := flags.String("expect-writer", "", "expected source writer ID")
	localID := flags.String("expect-local-id", "", "expected immutable physical executor ID")
	out := flags.String("output", "", "new private output directory")
	bundle := flags.String("bundle", "", "private source bundle directory")
	bundleDigest := flags.String("bundle-sha256", "", "reviewed bundle SHA256")
	transition := flags.String("transition", "", "private reader transition file")
	transitionDigest := flags.String("transition-sha256", "", "reviewed transition SHA256")
	sourceURL := flags.String("source-peer-url", "", "verified physical former-writer HTTPS origin")
	sourcePin := flags.String("source-connect-to", "", "physical former-writer IP pin")
	stage := flags.String("prepared", "", "prepared state directory")
	planDigest := flags.String("plan-sha256", "", "reviewed exact plan SHA256")
	backup := flags.String("backup", "", "private rollback/operation directory")
	envFile := flags.String("environment-file", "", "reviewed final-precedence service environment file")
	entrypoint := flags.String("identity-derived-entrypoint", "", "reviewed HAOS member entrypoint deriving source ID from node identity")
	entrypointDigest := flags.String("entrypoint-sha256", "", "exact reviewed identity-derived entrypoint SHA256")
	seedAlias := flags.String("identity-derived-seed-alias", "", "exact reviewed existing HAOS bootstrap alias; canonical identity remains primary")
	identityOptions := flags.String("identity-derived-options", "", "actual HAOS options file retaining the reviewed bootstrap alias")
	identityOptionsDigest := flags.String("options-sha256", "", "exact reviewed identity-derived options SHA256")
	listenHost := flags.String("listen-host", "", "optional reviewed listener IP, preserving the existing port")
	physical := flags.String("physical-identity", "", "configured executor shellmcp_identity.json; defaults to the actual executor environment")
	baseline := flags.String("retained-legacy-baseline", "", "frozen pre-forward physical-owner baseline; records remain on that node")
	baselineDigest := flags.String("retained-legacy-baseline-sha256", "", "exact frozen baseline digest")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	cfg := hub.FromEnv()
	if *physical == "" {
		root := os.Getenv("SHELL_IDENTITY_DIR")
		if root == "" {
			root = os.Getenv("SHELLMCP_IDENTITY_DIR")
		}
		if root == "" {
			root = cfg.ConfigDir
		}
		*physical = filepath.Join(root, "shellmcp_identity.json")
	}
	{
		if *configDir == "" {
			return errors.New("explicit --config-dir is required")
		}
		a, err := filepath.Abs(*configDir)
		if err != nil {
			return err
		}
		b, err := filepath.Abs(cfg.ConfigDir)
		if err != nil {
			return err
		}
		if a != b {
			return errors.New("config directory differs from the effective service environment")
		}
	}
	var value any
	var err error
	switch action {
	case "preflight":
		value, err = hub.InspectAuthorityConfiguration(cfg, *target, *localID, *physical)
	case "capture-legacy-baseline":
		value, err = hub.CaptureStoppedLegacyBaseline(cfg, *target, *localID, *physical, *out)
	case "export":
		value, err = hub.ExportStoppedAuthority(cfg, hub.AuthorityHandoffExportOptions{SourceTarget: *target, ExpectedWriter: *writerID, OutputDir: *out, PhysicalIdentityFile: *physical, LegacyBaselineFile: *baseline, LegacyBaselineDigest: *baselineDigest})
	case "prepare-writer":
		if *envFile == "" {
			return errors.New("runtime preparation requires --environment-file")
		}
		value, err = hub.PrepareStoppedAuthorityWriter(cfg, hub.AuthorityHandoffPrepareOptions{BundleDir: *bundle, BundleDigest: *bundleDigest, Target: *target, ExpectedLocalID: *localID, SourcePeerURL: *sourceURL, SourceConnectTo: *sourcePin, OutputDir: *out, EnvironmentFile: *envFile, PhysicalIdentityFile: *physical})
	case "prepare-reader":
		if *envFile == "" && *entrypoint == "" {
			return errors.New("runtime preparation requires a persistent environment source")
		}
		value, err = hub.PrepareStoppedAuthorityReader(cfg, hub.AuthorityReaderPrepareOptions{TransitionFile: *transition, TransitionDigest: *transitionDigest, Target: *target, ExpectedLocalID: *localID, OutputDir: *out, EnvironmentFile: *envFile, IdentityDerivedEntrypoint: *entrypoint, EntrypointDigest: *entrypointDigest, IdentityDerivedSeedAlias: *seedAlias, IdentityDerivedOptionsFile: *identityOptions, IdentityDerivedOptionsDigest: *identityOptionsDigest, ListenHost: *listenHost, PhysicalIdentityFile: *physical})
	case "apply":
		value, err = hub.ApplyStoppedAuthority(cfg, hub.AuthorityHandoffApplyOptions{PreparedDir: *stage, PlanDigest: *planDigest, BackupDir: *backup, RequireEnvironment: true, RequirePhysicalIdentity: true})
	case "authorize-activation":
		err = hub.MarkAuthorityActivation(cfg, *backup, *planDigest)
		value = map[string]any{"plan_digest": *planDigest, "snapshot_rollback_retired": err == nil}
	case "restore-before-activation":
		err = hub.RestoreStoppedAuthorityBeforeActivation(cfg, *backup, *planDigest)
		value = map[string]any{"plan_digest": *planDigest, "restored": err == nil, "immutable_legacy_history_retained": true}
	case "inspect":
		value, err = hub.InspectAuthorityHandoff(cfg, *backup, *planDigest)
	default:
		return errors.New("unknown handoff action")
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(value)
}
