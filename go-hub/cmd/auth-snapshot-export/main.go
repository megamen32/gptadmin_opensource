// auth-snapshot-export is a read-only legacy bootstrap, never a Hub runtime.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/megamen32/gptadmin/go-hub/internal/hub"
)

func main() {
	dir := flag.String("config-dir", "", "existing legacy authority directory")
	identity := flag.String("writer-id", "", "existing primary server_id (required)")
	output := flag.String("output", "", "new private output file outside source directory")
	budget := flag.Int("max-bytes", 262144, "whole snapshot byte limit, maximum 10485760")
	flag.Parse()
	if *dir == "" || *identity == "" || *output == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "required: --config-dir --writer-id --output; CTL_TOKEN from environment")
		os.Exit(2)
	}
	if err := hub.ExportLegacyAuthSnapshot(*dir, *identity, *output, *budget, os.Getenv("CTL_TOKEN")); err != nil {
		fmt.Fprintln(os.Stderr, "auth snapshot export:", err)
		os.Exit(1)
	}
	// Do not print bundle contents or credentials, including on success.
	fmt.Fprintln(os.Stderr, "bootstrap generation 1 exported; apply only to an unseeded reader")
}
