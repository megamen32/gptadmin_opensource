// Local-only browser test fixture. It never loads the production environment.
package main

import (
	"github.com/megamen32/gptadmin/go-hub/internal/hub"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if len(os.Args) != 3 {
		log.Fatal("usage: canary TEST_DIRECTORY LOOPBACK_ADDRESS")
	}
	root, addr := os.Args[1], os.Args[2]
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		log.Fatal("canary must bind to loopback")
	}
	cfg := hub.Config{Addr: addr, ConfigDir: filepath.Join(root, "config"), PublicDir: filepath.Join(root, "public"), ArtifactDir: filepath.Join(root, "build"), OutputDir: filepath.Join(root, "outputs"), EnvFile: filepath.Join(root, "fixture.env"), AdminPassword: "local-ui-canary", PublicOrigin: "http://" + addr, DefaultTimeout: 10 * time.Second, PollMaxTimeout: time.Second}
	// Handler only: no fleet maintenance or updater loop in this fixture.
	log.Fatal(http.ListenAndServe(addr, hub.New(cfg).Handler()))
}
