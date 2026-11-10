package hub

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The client contains no credential. Each download uses the current grant.
//
//go:embed bootstrap_assets
var externalBootstrapAssets embed.FS

func bootstrapAuditPath(path string) string {
	if strings.HasPrefix(path, "/bootstrap/") {
		return "/bootstrap/[redacted]"
	}
	return path
}

func bootstrapAuditReferer(value string) string {
	parsed, err := url.Parse(value)
	if (err == nil && strings.HasPrefix(parsed.Path, "/bootstrap/")) || strings.Contains(value, "/bootstrap/") {
		return "[redacted bootstrap URL]"
	}
	return value
}

func bootstrapNoStore(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, private, max-age=0")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		next(w, r)
	}
}

func bootstrapLinkView(record managedMCPToken, now int64) map[string]any {
	status := "active"
	if record.RevokedAt != 0 {
		status = "revoked"
	} else if record.ExpiresAt <= now {
		status = "expired"
	}
	return map[string]any{"id": record.ID, "label": record.BootstrapLabel, "profile_id": record.ProfileID, "client_id": record.ClientID, "token_id": record.ID, "created_at": record.IssuedAt, "expires_at": record.ExpiresAt, "revoked_at": record.RevokedAt, "status": status}
}

// Grants live in the existing durable access ledger. Expiry, revocation,
// replication and profile checks therefore also cover downloaded credentials.
func (s *Server) adminBootstrapLinks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodDelete {
		writeJSON(w, 405, map[string]any{"detail": "use GET, POST or DELETE"})
		return
	}
	action := map[string]string{http.MethodGet: "list", http.MethodPost: "issue", http.MethodDelete: "revoke"}[r.Method]
	if err := authorizeToolAction(r, "hub", "access_clients", map[string]any{"action": action}); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": "access profile denies this action"})
		return
	}
	if err := s.refreshAccessState(); err != nil {
		writeJSON(w, 503, map[string]any{"detail": "access state unavailable"})
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/admin/api/bootstrap-links")
	if r.Method == http.MethodDelete {
		id = strings.TrimPrefix(id, "/")
		if id == "" || strings.ContainsAny(id, "/\\") {
			writeJSON(w, 400, map[string]any{"detail": "link id required"})
			return
		}
		s.mu.Lock()
		record, ok := s.managedMCP[id]
		if !ok || record.BootstrapDigest == "" {
			s.mu.Unlock()
			writeJSON(w, 404, map[string]any{"detail": "link not found"})
			return
		}
		if record.RevokedAt == 0 {
			record.RevokedAt = s.now().Unix()
		}
		s.managedMCP[id] = record
		err := s.saveManagedMCPStateLocked()
		s.mu.Unlock()
		if err != nil {
			writeJSON(w, 409, map[string]any{"detail": "could not persist revocation; refresh before retrying"})
			return
		}
		s.authAudit("bootstrap_revoked", r, map[string]any{"token_id": id})
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	if id != "" && id != "/" {
		writeJSON(w, 404, map[string]any{"detail": "not found"})
		return
	}
	if r.Method == http.MethodGet {
		s.mu.Lock()
		items := make([]map[string]any, 0)
		for _, record := range s.managedMCP {
			if record.BootstrapDigest != "" {
				items = append(items, bootstrapLinkView(record, s.now().Unix()))
			}
		}
		s.mu.Unlock()
		sort.Slice(items, func(i, j int) bool { return items[i]["created_at"].(int64) > items[j]["created_at"].(int64) })
		writeJSON(w, 200, map[string]any{"items": items})
		return
	}
	var req struct {
		Label     string `json:"label"`
		ProfileID string `json:"profile_id"`
		TTLDays   *int   `json:"ttl_days"`
	}
	if err := readBoundedJSON(r, &req); err != nil {
		writeJSON(w, 400, map[string]any{"detail": "invalid link request"})
		return
	}
	ttl := 7
	if req.TTLDays != nil {
		ttl = *req.TTLDays
	}
	req.Label = strings.TrimSpace(req.Label)
	req.ProfileID = strings.TrimSpace(req.ProfileID)
	if req.Label == "" {
		req.Label = "Внешний агент"
	}
	if ttl < 1 || ttl > 3650 || len(req.Label) > 512 || strings.ContainsAny(req.Label, "\r\n\x00") || req.ProfileID == "" {
		writeJSON(w, 400, map[string]any{"detail": "profile_id is required; ttl_days must be 1..3650; label must fit 512 bytes"})
		return
	}
	profiles, err := s.accessProfilesSnapshot()
	if err != nil {
		writeAccessProfileError(w, err)
		return
	}
	profile, ok := profiles[req.ProfileID]
	if !ok {
		writeJSON(w, 404, map[string]any{"detail": "access profile not found"})
		return
	}
	// Validate the exact documents before minting access, without running tools.
	if _, _, err = s.bootstrapDocuments(r, profile); err != nil {
		writeJSON(w, 503, map[string]any{"detail": err.Error()})
		return
	}
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		writeJSON(w, 500, map[string]any{"detail": "could not generate link"})
		return
	}
	secretText := base64.RawURLEncoding.EncodeToString(secret)
	digest := sha256.Sum256([]byte(secretText))
	_, record, err := newManagedMCPToken("external-"+newID(), ttl, s.origin(r), s.resource(r), profile.AccessMode, profile.ID, "client", s.now().Unix())
	if err != nil {
		writeJSON(w, 500, map[string]any{"detail": "could not issue access"})
		return
	}
	record.BootstrapDigest = hex.EncodeToString(digest[:])
	record.BootstrapLabel = req.Label
	s.mu.Lock()
	s.managedMCP[record.ID] = record
	err = s.saveManagedMCPStateLocked()
	s.mu.Unlock()
	if err != nil {
		writeJSON(w, 409, map[string]any{"detail": "could not persist access; refresh before retrying"})
		return
	}
	linkURL := strings.TrimRight(record.Issuer, "/") + "/bootstrap/" + record.ID + "." + secretText
	prompt := "Прочитай инструкцию по ссылке " + linkURL + " и следуй ей. Скачай готовый клиент, определи проект через discover/search, при наличии Agent Herder зарегистрируй область работы по инструкции, затем выполняй мою задачу: "
	s.authAudit("bootstrap_issued", r, map[string]any{"token_id": record.ID, "profile_id": record.ProfileID, "expires_at": record.ExpiresAt})
	writeJSON(w, 201, map[string]any{"link": bootstrapLinkView(record, s.now().Unix()), "url": linkURL, "prompt": prompt, "download_url": linkURL + "/client.py"})
}

func (s *Server) externalBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeJSON(w, 405, map[string]any{"detail": "use GET or HEAD"})
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/bootstrap/"), "/")
	if len(parts) < 1 || len(parts) > 2 || r.URL.RawQuery != "" {
		writeJSON(w, 404, map[string]any{"detail": "link not found"})
		return
	}
	key := strings.SplitN(parts[0], ".", 2)
	if len(key) != 2 || len(key[1]) != 43 {
		writeJSON(w, 404, map[string]any{"detail": "link not found"})
		return
	}
	if err := s.refreshAccessState(); err != nil {
		writeJSON(w, 503, map[string]any{"detail": "access state unavailable"})
		return
	}
	s.mu.Lock()
	record, ok := s.managedMCP[key[0]]
	s.mu.Unlock()
	digest := sha256.Sum256([]byte(key[1]))
	expected, err := hex.DecodeString(record.BootstrapDigest)
	if !ok || err != nil || subtle.ConstantTimeCompare(expected, digest[:]) != 1 {
		writeJSON(w, 404, map[string]any{"detail": "link not found"})
		return
	}
	if record.RevokedAt != 0 || record.ExpiresAt <= s.now().Unix() || record.TokenValue == "" || record.ProfileID == "" {
		writeJSON(w, 410, map[string]any{"detail": "link expired or revoked"})
		return
	}
	profiles, err := s.accessProfilesSnapshot()
	if err != nil {
		writeJSON(w, 503, map[string]any{"detail": "access state unavailable"})
		return
	}
	profile, ok := profiles[record.ProfileID]
	if !ok {
		writeJSON(w, 410, map[string]any{"detail": "link profile is unavailable"})
		return
	}
	asset := ""
	if len(parts) == 2 {
		asset = parts[1]
	}
	if asset != "" && asset != "instructions.md" && asset != "config.json" && asset != "manifest.json" && asset != "client.py" {
		writeJSON(w, 404, map[string]any{"detail": "not found"})
		return
	}
	// All downloads require the current instructions, including configuration
	// and HEAD requests. Do not release access with an incomplete workflow.
	doc, workflow, err := s.bootstrapDocuments(r, profile)
	if err != nil {
		writeJSON(w, 503, map[string]any{"detail": err.Error()})
		return
	}
	if asset == "config.json" {
		writeBootstrapJSON(w, r, map[string]any{"mcp_url": strings.TrimRight(record.Issuer, "/") + "/mcp", "access_token": record.TokenValue, "protocol_version": mcpProtocolVersion, "expires_at": record.ExpiresAt, "profile_id": record.ProfileID})
		return
	}
	if asset == "client.py" {
		data, err := externalBootstrapAssets.ReadFile("bootstrap_assets/gptadmin.py")
		if err != nil {
			writeJSON(w, 503, map[string]any{"detail": "client asset unavailable"})
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="gptadmin.py"`)
		writeBootstrapBytes(w, r, "text/x-python; charset=utf-8", data)
		return
	}
	if asset == "manifest.json" {
		client, err := externalBootstrapAssets.ReadFile("bootstrap_assets/gptadmin.py")
		if err != nil {
			writeJSON(w, 503, map[string]any{"detail": "client asset unavailable"})
			return
		}
		sha := sha256.Sum256(client)
		writeBootstrapJSON(w, r, map[string]any{"protocol_version": mcpProtocolVersion, "client_sha256": hex.EncodeToString(sha[:]), "client_bytes": len(client), "instruction_version": doc.Version, "lhc": workflow.meta, "profile_id": profile.ID, "expires_at": record.ExpiresAt})
		return
	}
	template, err := externalBootstrapAssets.ReadFile("bootstrap_assets/instructions.md")
	if err != nil {
		writeJSON(w, 503, map[string]any{"detail": "instructions asset unavailable"})
		return
	}
	linkURL := strings.TrimRight(record.Issuer, "/") + "/bootstrap/" + parts[0]
	body := strings.NewReplacer("{{BOOTSTRAP_URL}}", linkURL, "{{EXPIRES_AT}}", time.Unix(record.ExpiresAt, 0).UTC().Format(time.RFC3339), "{{PROFILE_ID}}", profile.ID, "{{PROTOCOL}}", mcpProtocolVersion, "{{INSTRUCTIONS}}", doc.Content, "{{LHC}}", workflow.text).Replace(string(template))
	writeBootstrapBytes(w, r, "text/markdown; charset=utf-8", []byte(body))
}

func writeBootstrapJSON(w http.ResponseWriter, r *http.Request, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		writeJSON(w, 500, map[string]any{"detail": "could not render bootstrap asset"})
		return
	}
	writeBootstrapBytes(w, r, "application/json; charset=utf-8", data)
}

func writeBootstrapBytes(w http.ResponseWriter, r *http.Request, contentType string, data []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

type bootstrapWorkflow struct {
	text string
	meta map[string]any
}

func (s *Server) bootstrapDocuments(r *http.Request, profile AccessProfile) (InstructionSet, bootstrapWorkflow, error) {
	doc, _, err := s.instructionDocument(requestWithAccessProfile(r, profile))
	if err != nil {
		return doc, bootstrapWorkflow{}, errors.New("profile instructions unavailable")
	}
	workflow, err := loadBootstrapWorkflow(s.cfg.BootstrapLHCPath)
	return doc, workflow, err
}

const bootstrapLHCName = "last-human-commit"

func parseBootstrapVersion(name string) ([3]uint64, bool) {
	var version [3]uint64
	parts := strings.Split(name, ".")
	if len(parts) != 3 {
		return version, false
	}
	for i, part := range parts {
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil || part == "" {
			return version, false
		}
		version[i] = n
	}
	return version, true
}

type bootstrapVersionDir struct {
	name    string
	version [3]uint64
}

func bootstrapVersionDirs(entries []os.DirEntry) []bootstrapVersionDir {
	var versions []bootstrapVersionDir
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if version, valid := parseBootstrapVersion(entry.Name()); valid {
			versions = append(versions, bootstrapVersionDir{entry.Name(), version})
		}
	}
	sort.SliceStable(versions, func(i, j int) bool {
		return slices.Compare(versions[i].version[:], versions[j].version[:]) > 0
	})
	return versions
}

func bootstrapCodexHome(root string) (home, market string) {
	marketPath := filepath.Dir(root)
	cache := filepath.Dir(marketPath)
	plugins := filepath.Dir(cache)
	if filepath.Base(root) != bootstrapLHCName || filepath.Base(cache) != "cache" || filepath.Base(plugins) != "plugins" {
		return "", ""
	}
	return filepath.Dir(plugins), filepath.Base(marketPath)
}

// Open each real directory under the configured Codex home. The package
// handle keeps manifest and document reads together across checkout renames.
func openBootstrapDirs(root *os.Root, names ...string) *os.Root {
	current := root
	for _, name := range names {
		info, err := current.Lstat(name)
		var next *os.Root
		if err == nil && info.IsDir() {
			next, _ = current.OpenRoot(name)
		}
		if current != root {
			current.Close()
		}
		if next == nil {
			return nil
		}
		if opened, err := next.Stat("."); err != nil || !os.SameFile(info, opened) {
			next.Close()
			return nil
		}
		current = next
	}
	return current
}

func loadCodexBootstrapRelease(pkg *os.Root, installedVersion string) (bootstrapWorkflow, [3]uint64) {
	manifestDir := openBootstrapDirs(pkg, ".codex-plugin")
	if manifestDir == nil {
		return bootstrapWorkflow{}, [3]uint64{}
	}
	defer manifestDir.Close()
	manifest, err := readBootstrapText("plugin.json", manifestDir.Lstat, manifestDir.Open, 64*1024)
	var meta struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err != nil || json.Unmarshal(manifest, &meta) != nil || meta.Name != bootstrapLHCName ||
		(installedVersion != "" && installedVersion != meta.Version) {
		return bootstrapWorkflow{}, [3]uint64{}
	}
	version, valid := parseBootstrapVersion(meta.Version)
	if !valid {
		return bootstrapWorkflow{}, [3]uint64{}
	}
	data, err := readBootstrapText("AGENTS.md", pkg.Lstat, pkg.Open, 256*1024)
	if err != nil {
		return bootstrapWorkflow{}, [3]uint64{}
	}
	path := filepath.Join(pkg.Name(), "AGENTS.md")
	return makeBootstrapWorkflow(path, pkg.Name(), meta.Version, data), version
}

func loadCodexBootstrapWorkflow(home, market string) (bootstrapWorkflow, error) {
	codex, err := os.OpenRoot(home)
	if err != nil {
		return bootstrapWorkflow{}, errors.New("configured LHC installation unavailable")
	}
	defer codex.Close()
	var published bootstrapWorkflow
	var publishedVersion [3]uint64
	if pkg := openBootstrapDirs(codex, ".tmp", "marketplaces", market, "plugins", bootstrapLHCName); pkg != nil {
		published, publishedVersion = loadCodexBootstrapRelease(pkg, "")
		pkg.Close()
	}
	if cache := openBootstrapDirs(codex, "plugins", "cache", market, bootstrapLHCName); cache != nil {
		defer cache.Close()
		entries, _ := fs.ReadDir(cache.FS(), ".")
		for _, candidate := range bootstrapVersionDirs(entries) {
			if published.meta != nil && slices.Compare(candidate.version[:], publishedVersion[:]) < 0 {
				break
			}
			if pkg := openBootstrapDirs(cache, candidate.name); pkg != nil {
				installed, _ := loadCodexBootstrapRelease(pkg, candidate.name)
				pkg.Close()
				if installed.meta != nil {
					return installed, nil
				}
			}
		}
	}
	if published.meta != nil {
		return published, nil
	}
	return bootstrapWorkflow{}, errors.New("no complete configured LHC release available")
}

func loadBootstrapWorkflow(root string) (bootstrapWorkflow, error) {
	if strings.TrimSpace(root) == "" || !filepath.IsAbs(root) {
		return bootstrapWorkflow{}, errors.New("active LHC installation is not configured")
	}
	// Recognize legacy version paths before resolving symlinks: the updater
	// may already have pruned them. Nonversioned file and active pins stay explicit.
	versionDir := root
	if filepath.Base(root) == "AGENTS.md" {
		versionDir = filepath.Dir(root)
	}
	_, versioned := parseBootstrapVersion(filepath.Base(versionDir))
	if versioned {
		root = filepath.Dir(versionDir)
	}
	if home, market := bootstrapCodexHome(root); home != "" {
		// The exact native cache root is a versions directory, even with
		// a stray AGENTS.md. A configured symlink is still explicit.
		if info, _ := os.Lstat(filepath.Clean(root)); info == nil || info.IsDir() {
			return loadCodexBootstrapWorkflow(home, market)
		}
	}
	path, err := filepath.EvalSymlinks(root)
	if err != nil {
		return bootstrapWorkflow{}, errors.New("configured LHC installation unavailable")
	}
	info, err := os.Stat(path)
	if err != nil {
		return bootstrapWorkflow{}, errors.New("configured LHC installation unavailable")
	}
	version := "configured"
	packagePath := filepath.Dir(path)
	if info.IsDir() {
		if _, statErr := os.Stat(filepath.Join(path, "AGENTS.md")); errors.Is(statErr, os.ErrNotExist) {
			// An ordinary versions root keeps its fail-closed newest-only policy.
			entries, err := os.ReadDir(path)
			versions := bootstrapVersionDirs(entries)
			if err != nil || len(versions) == 0 {
				return bootstrapWorkflow{}, errors.New("configured LHC installation unavailable")
			}
			path = filepath.Join(path, versions[0].name)
		} else if statErr != nil {
			return bootstrapWorkflow{}, errors.New("configured LHC instructions unavailable")
		}
		packagePath = path
		version = filepath.Base(path)
		path = filepath.Join(path, "AGENTS.md")
	}
	data, err := readBootstrapText(path, os.Stat, os.Open, 256*1024)
	if err != nil {
		return bootstrapWorkflow{}, err
	}
	return makeBootstrapWorkflow(path, packagePath, version, data), nil
}

func readBootstrapText(path string, stat func(string) (os.FileInfo, error), open func(string) (*os.File, error), limit int64) ([]byte, error) {
	info, err := stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("configured LHC instructions must be a regular file")
	}
	f, err := open(path)
	if err != nil {
		return nil, errors.New("configured LHC instructions unavailable")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, errors.New("configured LHC instructions unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || len(data) == 0 || int64(len(data)) > limit || !utf8.Valid(data) {
		return nil, errors.New("configured LHC instructions invalid or too large")
	}
	return data, nil
}

func makeBootstrapWorkflow(path, packagePath, version string, data []byte) bootstrapWorkflow {
	sha := sha256.Sum256(data)
	hash := hex.EncodeToString(sha[:])
	return bootstrapWorkflow{text: fmt.Sprintf("Источник LHC: %s. Корень пакета: %s. Версия пакета: %s. SHA-256: %s.\n\n%s", path, packagePath, version, hash, data), meta: map[string]any{"source_path": path, "package_path": packagePath, "version": version, "sha256": hash, "bytes": len(data)}}
}
