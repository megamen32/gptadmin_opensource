// Command networkticket creates one short-lived role-bound ticket for a
// controlled Network Tunnel bring-up. Production issuance belongs to the Hub.
package main

import (
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/megamen32/gptadmin/go-proxyrelay/internal/ticket"
)

func main() {
	keyFile := flag.String("key-file", "", "relay signing key file")
	output := flag.String("output", "", "0600 output file; stdout when empty")
	role := flag.String("role", "", "ticket role: client or agent")
	capabilityID := flag.String("capability-id", "bringup", "capability identifier")
	streamID := flag.String("stream-id", "", "stream identifier shared by both role tickets")
	profileID := flag.String("profile-id", "bringup", "admin profile identifier")
	agentID := flag.String("agent-id", "edge", "edge agent identifier")
	target := flag.String("target", "", "target host:port")
	expiresIn := flag.Duration("expires-in", 5*time.Minute, "ticket lifetime")
	flag.Parse()

	if strings.TrimSpace(*keyFile) == "" || strings.TrimSpace(*role) == "" || strings.TrimSpace(*streamID) == "" || strings.TrimSpace(*target) == "" {
		fatal("-key-file, -role, -stream-id, and -target are required")
	}
	if *expiresIn <= 0 {
		fatal("-expires-in must be positive")
	}
	if *role != ticket.RoleClient && *role != ticket.RoleAgent {
		fatal("-role must be client or agent")
	}
	key, err := os.ReadFile(*keyFile)
	if err != nil {
		fatal("read relay key: %v", err)
	}
	signer, err := ticket.NewSigner([]byte(strings.TrimRight(string(key), " \t\r\n")))
	if err != nil {
		fatal("create ticket signer: %v", err)
	}
	jti, err := randomID()
	if err != nil {
		fatal("create ticket id: %v", err)
	}
	now := time.Now().UTC()
	raw, err := signer.SignStream(ticket.Claims{
		Kind:            ticket.KindStream,
		ProtocolVersion: 1,
		CapabilityID:    strings.TrimSpace(*capabilityID),
		StreamID:        strings.TrimSpace(*streamID),
		ProfileID:       strings.TrimSpace(*profileID),
		AgentID:         strings.TrimSpace(*agentID),
		Target:          strings.TrimSpace(*target),
		Role:            *role,
		ExpiresAt:       now.Add(*expiresIn).Unix(),
		JTI:             jti,
		Limits: ticket.Limits{
			MaxFrameBytes:            32 * 1024,
			MaxPendingFrames:         16,
			DialTimeoutSeconds:       10,
			IdleTimeoutSeconds:       300,
			MaxStreamLifetimeSeconds: 3600,
			MaxBytes:                 1 << 30,
			MaxStreamsPerAgent:       1,
			MaxStreamsPerProfile:     1,
		},
	})
	if err != nil {
		fatal("sign ticket: %v", err)
	}
	if *output == "" {
		fmt.Println(raw)
		return
	}
	if err := os.WriteFile(*output, []byte(raw+"\n"), 0o600); err != nil {
		fatal("write ticket: %v", err)
	}
}

func randomID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}
