package cloudos

// Computer represents a connected local computer.
type Computer struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	OS           string   `json:"os"`
	Capabilities []string `json:"capabilities"` // "files", "processes", "network"
	Status       string   `json:"status"`       // "online", "offline"
	LastSeen     string   `json:"last_seen"`
	SessionID    string   `json:"session_id"`
	TunnelID     string   `json:"tunnel_id,omitempty"`
	PeerID       string   `json:"peer_id,omitempty"`
	Endpoint     string   `json:"endpoint,omitempty"`
	AgentToken   string   `json:"-"`
}

// ComputerOperation is the standard request envelope for all Cloud OS
// operations routed through the hub.
type ComputerOperation struct {
	Computer  string `json:"computer"`
	Session   string `json:"session"`
	Operation string `json:"operation"`
	Path      string `json:"path,omitempty"`
	Content   string `json:"content,omitempty"`
	Command   string `json:"command,omitempty"`
	PID       int    `json:"pid,omitempty"`
	Signal    string `json:"signal,omitempty"`
	Host      string `json:"host,omitempty"`
	Port      int    `json:"port,omitempty"`
	ProxyType string `json:"proxy_type,omitempty"` // "tcp", "http", "socks5"
}

// FileEntry represents a file or directory returned by the native host.
type FileEntry struct {
	Name     string `json:"name"`
	Type     string `json:"type"` // "file", "directory"
	Size     int64  `json:"size,omitempty"`
	Modified string `json:"modified,omitempty"`
	Path     string `json:"path"`
}

// ProcessEntry represents a running process on a remote computer.
type ProcessEntry struct {
	PID    int     `json:"pid"`
	Name   string  `json:"name"`
	User   string  `json:"user"`
	CPU    float64 `json:"cpu"`
	Memory string  `json:"memory"`
	Status string  `json:"status"`
}

// ExecResult is the result of a process execution on a remote computer.
type ExecResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}
