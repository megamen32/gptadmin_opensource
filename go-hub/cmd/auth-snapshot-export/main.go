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
	stopped := flag.String("stopped-writer-dir", "", "stopped continuity writer config directory")
	identity := flag.String("writer-id", "", "existing primary server_id (required)")
	output := flag.String("output", "", "new private output file outside source directory")
	budget := flag.Int("max-bytes", 262144, "whole snapshot byte limit, maximum 10485760")
	flag.Parse()
	if (*dir == "") == (*stopped == "") || *identity == "" || *output == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "required: exactly one of --config-dir or --stopped-writer-dir, plus --writer-id and --output; CTL_TOKEN from environment")
		os.Exit(2)
	}
	var err error
	if *stopped != "" {
		err = hub.ExportStoppedWriterAuthSnapshot(*stopped, *identity, *output, *budget, os.Getenv("CTL_TOKEN"))
	} else {
		err = hub.ExportLegacyAuthSnapshot(*dir, *identity, *output, *budget, os.Getenv("CTL_TOKEN"))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "auth snapshot export:", err)
		os.Exit(1)
	}
	// Do not print bundle contents or credentials, including on success.
	if *stopped != "" {
		fmt.Fprintln(os.Stderr, "stopped writer generation exported; keep the writer fenced")
	} else {
		fmt.Fprintln(os.Stderr, "bootstrap generation 1 exported; apply only to an unseeded reader")
	}
}
