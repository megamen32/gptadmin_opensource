package cloudos

import (
        "net/http"
        "strings"
)

// RegisterRoutes adds all Cloud OS API routes to mux. The registry and
// tunnelMgr dependencies are shared across every handler.
//
// Routes are organized into four groups:
//
//   1. Computer management: list, status, pair, heartbeat, disconnect, ticket
//   2. File operations: list, read, write (relayed as filesystem streams)
//   3. Process operations: list, exec, kill (relayed as process streams)
//   4. Network: connect (delegates to existing NetworkProxyController)
//
// The stream relay uses the existing go-proxyrelay infrastructure:
//   - control/filesystem/process streams: Cloud OS signs its own gpr1 tickets
//   - network streams: delegated to NetworkProxyController (existing flow)
func RegisterRoutes(mux *http.ServeMux, registry *Registry, tunnelMgr TunnelManager) {
        // ---- Computer management ----

        // GET /api/v1/cloud-os/computers — list all computers
        mux.HandleFunc("/api/v1/cloud-os/computers", func(w http.ResponseWriter, r *http.Request) {
                switch r.Method {
                case http.MethodGet:
                        HandleComputerList(registry)(w, r)
                default:
                        writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
                                "detail": "method not allowed",
                        })
                }
        })

        // Subtree under /api/v1/cloud-os/computers/ for single-computer ops.
        mux.HandleFunc("/api/v1/cloud-os/computers/", func(w http.ResponseWriter, r *http.Request) {
                switch {
                case r.URL.Path == "/api/v1/cloud-os/computers/pair" && r.Method == http.MethodPost:
                        HandleComputerPair(registry, tunnelMgr)(w, r)
                case strings.HasSuffix(r.URL.Path, "/disconnect") && r.Method == http.MethodPost:
                        HandleComputerDisconnect(registry, tunnelMgr)(w, r)
                case strings.HasSuffix(r.URL.Path, "/ticket") && r.Method == http.MethodPost:
                        HandleAgentTicket(tunnelMgr)(w, r)
                case strings.HasSuffix(r.URL.Path, "/heartbeat") && r.Method == http.MethodPost:
                        HandleHeartbeat(registry)(w, r)
                case r.Method == http.MethodGet:
                        HandleComputerStatus(registry, tunnelMgr)(w, r)
                default:
                        writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
                                "detail": "method not allowed",
                        })
                }
        })

        // ---- File operations (filesystem streams via proxyrelay) ----

        mux.HandleFunc("/api/v1/cloud-os/files/list", requirePOST(HandleFilesList(registry, tunnelMgr)))
        mux.HandleFunc("/api/v1/cloud-os/files/read", requirePOST(HandleFilesRead(registry, tunnelMgr)))
        mux.HandleFunc("/api/v1/cloud-os/files/write", requirePOST(HandleFilesWrite(registry, tunnelMgr)))

        // ---- Process operations (process streams via proxyrelay) ----

        mux.HandleFunc("/api/v1/cloud-os/processes/list", requirePOST(HandleProcessesList(registry, tunnelMgr)))
        mux.HandleFunc("/api/v1/cloud-os/processes/exec", requirePOST(HandleProcessesExec(registry, tunnelMgr)))
        mux.HandleFunc("/api/v1/cloud-os/processes/kill", requirePOST(HandleProcessesKill(registry, tunnelMgr)))

        // ---- Network (delegates to existing NetworkProxyController) ----

        mux.HandleFunc("/api/v1/cloud-os/network/connect", requirePOST(HandleNetworkConnect(registry, tunnelMgr)))
}
