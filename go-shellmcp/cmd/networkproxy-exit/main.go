// Command networkproxy-exit exposes a loopback HTTP CONNECT/SOCKS5 proxy whose
// per-connection grants are issued for the ShellMCP exit node selected locally.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/networkproxy"
)

type exitNode struct {
	AgentID      string `json:"agent_id"`
	Name         string `json:"name"`
	CapabilityID string `json:"capability_id"`
}
type exitNodesFile struct {
	Nodes []exitNode `json:"nodes"`
}
type selector struct {
	mu       sync.RWMutex
	nodes    map[string]exitNode
	selected string
}

func main() {
	listen := flag.String("listen", "127.0.0.1:3126", "local HTTP CONNECT/SOCKS5 proxy address")
	control := flag.String("control-listen", "127.0.0.1:3127", "loopback selector API address")
	hub := flag.String("hub", "", "Hub base URL")
	relay := flag.String("relay", "", "Network Tunnel relay WebSocket URL")
	tokenFile := flag.String("ctl-token-file", "", "file containing Hub control bearer token")
	nodesFile := flag.String("nodes-file", "", "JSON file containing authorized exit nodes")
	flag.Parse()
	if *hub == "" || *relay == "" || *tokenFile == "" || *nodesFile == "" {
		log.Fatal("-hub, -relay, -ctl-token-file, and -nodes-file are required")
	}
	token, err := os.ReadFile(*tokenFile)
	if err != nil {
		log.Fatal(err)
	}
	nodes, err := loadNodes(*nodesFile)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go serveControl(ctx, *control, nodes)
	source := hubTicketSource(strings.TrimRight(*hub, "/"), *relay, strings.TrimSpace(string(token)), nodes)
	if err := networkproxy.ListenAndServeLocalProxy(ctx, *listen, networkproxy.LocalProxyConfig{AllowAnyTarget: true, MaxFrameBytes: 32 * 1024, WriteTimeout: 10 * time.Second, TicketSource: source}); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}

func loadNodes(path string) (*selector, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file exitNodesFile
	if err := json.Unmarshal(b, &file); err != nil {
		return nil, err
	}
	s := &selector{nodes: map[string]exitNode{}}
	for _, node := range file.Nodes {
		node.AgentID = strings.TrimSpace(node.AgentID)
		node.CapabilityID = strings.TrimSpace(node.CapabilityID)
		if node.AgentID == "" || node.CapabilityID == "" {
			return nil, errors.New("each exit node requires agent_id and capability_id")
		}
		s.nodes[node.AgentID] = node
	}
	if len(s.nodes) == 0 {
		return nil, errors.New("no exit nodes configured")
	}
	return s, nil
}

func (s *selector) choose(agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.nodes[agentID]; !ok {
		return errors.New("exit node is not authorized")
	}
	s.selected = agentID
	return nil
}
func (s *selector) current() (exitNode, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.nodes[s.selected]
	return n, ok
}
func (s *selector) list() []exitNode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]exitNode, 0, len(s.nodes))
	for _, n := range s.nodes {
		out = append(out, n)
	}
	return out
}

func serveControl(ctx context.Context, address string, selected *selector) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/exit-nodes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		writeJSON(w, map[string]any{"nodes": selected.list()})
	})
	mux.HandleFunc("/v1/selection", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "method not allowed", 405)
			return
		}
		var body struct {
			AgentID string `json:"agent_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || selected.choose(body.AgentID) != nil {
			http.Error(w, "unknown exit node", 400)
			return
		}
		node, _ := selected.current()
		writeJSON(w, map[string]any{"selected": node.AgentID})
	})
	mux.HandleFunc("/v1/status", func(w http.ResponseWriter, r *http.Request) {
		node, ok := selected.current()
		writeJSON(w, map[string]any{"selected": node.AgentID, "ready": ok, "proxy": "127.0.0.1:3126"})
	})
	server := &http.Server{Addr: address, Handler: mux}
	go func() { <-ctx.Done(); _ = server.Shutdown(context.Background()) }()
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("exit proxy control API stopped: %v", err)
	}
}

func hubTicketSource(hub, relay, token string, selected *selector) networkproxy.TicketSource {
	client := &http.Client{Timeout: 10 * time.Second}
	return func(ctx context.Context, target string) (string, string, error) {
		node, ok := selected.current()
		if !ok {
			return "", "", errors.New("select an exit node first")
		}
		body, _ := json.Marshal(map[string]string{"capability_id": node.CapabilityID, "target": target})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, hub+"/proxy-control/v1/issue", strings.NewReader(string(body)))
		if err != nil {
			return "", "", err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if err != nil {
			return "", "", err
		}
		defer response.Body.Close()
		var payload struct {
			ClientGrant struct {
				Token string `json:"token"`
			} `json:"client_grant"`
			Detail string `json:"detail"`
		}
		if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
			return "", "", err
		}
		if response.StatusCode != http.StatusOK || payload.ClientGrant.Token == "" {
			return "", "", fmt.Errorf("Hub grant issue failed: %s", payload.Detail)
		}
		return relay, payload.ClientGrant.Token, nil
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
