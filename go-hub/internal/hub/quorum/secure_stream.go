package quorum

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/hashicorp/raft"
)

const maxAuthFrame = 4096

type authHello struct {
	ClusterID string `json:"cluster_id"`
	NodeID    string `json:"node_id"`
	NodeKey   string `json:"node_key"`
}

// secureStream provides Raft with TLS server authentication and a second
// identity check using the already-enrolled GPTAdmin node key. The node key is
// sent only inside the verified TLS channel; it is never logged or persisted by
// this package.
type secureStream struct {
	listener    net.Listener
	localID     string
	clusterID   string
	localKey    string
	deadline    time.Duration
	roots       *x509.CertPool
	peersByAddr map[raft.ServerAddress]Peer
	peersByID   map[string]Peer
	mu          sync.RWMutex
}

func newSecureStream(c Config) (*secureStream, error) {
	cert, err := tls.LoadX509KeyPair(c.TLSCertFile, c.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("quorum: load TLS identity: %w", err)
	}
	roots, err := systemRootsWithFile(c.TLSCAFile)
	if err != nil {
		return nil, err
	}
	base := &secureStream{
		localID: c.NodeID, clusterID: c.ClusterID, localKey: c.NodeKey,
		deadline: c.RPCDeadline, roots: roots, peersByAddr: make(map[raft.ServerAddress]Peer, len(c.Peers)),
		peersByID: make(map[string]Peer, len(c.Peers)),
	}
	for _, peer := range c.Peers {
		base.peersByAddr[raft.ServerAddress(peer.Address)] = peer
		base.peersByID[peer.ID] = peer
	}
	listener, err := net.Listen("tcp", c.BindAddress)
	if err != nil {
		return nil, fmt.Errorf("quorum: listen: %w", err)
	}
	serverTLS := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
	base.listener = tls.NewListener(listener, serverTLS)
	return base, nil
}

func systemRootsWithFile(path string) (*x509.CertPool, error) {
	var roots *x509.CertPool
	if path == "" {
		return nil, nil // nil selects the platform's system roots in crypto/tls.
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	ca, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("quorum: read TLS CA: %w", err)
	}
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("quorum: TLS CA file contains no certificates")
	}
	return roots, nil
}

func (s *secureStream) Accept() (net.Conn, error) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return nil, err
		}
		tlsConn, ok := conn.(*tls.Conn)
		if !ok {
			_ = conn.Close()
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), s.deadline)
		err = tlsConn.HandshakeContext(ctx)
		cancel()
		if err != nil {
			_ = tlsConn.Close()
			continue
		}
		if err = tlsConn.SetDeadline(time.Now().Add(s.deadline)); err != nil {
			_ = tlsConn.Close()
			continue
		}
		var hello authHello
		if err = readAuthFrame(tlsConn, &hello); err != nil || !s.authorized(hello) {
			_ = tlsConn.Close()
			continue
		}
		if _, err = tlsConn.Write([]byte{1}); err != nil {
			_ = tlsConn.Close()
			continue
		}
		if err = tlsConn.SetDeadline(time.Time{}); err != nil {
			_ = tlsConn.Close()
			continue
		}
		return tlsConn, nil
	}
}

func (s *secureStream) Dial(address raft.ServerAddress, timeout time.Duration) (net.Conn, error) {
	s.mu.RLock()
	peer, ok := s.peersByAddr[address]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("quorum: unknown Raft peer address")
	}
	if timeout <= 0 || timeout > s.deadline {
		timeout = s.deadline
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: timeout}, "tcp", string(address), &tls.Config{
		ServerName: peer.ServerName,
		RootCAs:    s.roots,
		MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		return nil, fmt.Errorf("quorum: secure peer dial failed: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if err = writeAuthFrame(conn, authHello{ClusterID: s.clusterID, NodeID: s.localID, NodeKey: s.localKey}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	var ack [1]byte
	if _, err = io.ReadFull(conn, ack[:]); err != nil || ack[0] != 1 {
		_ = conn.Close()
		if err != nil {
			return nil, fmt.Errorf("quorum: peer authentication failed: %w", err)
		}
		return nil, errors.New("quorum: peer rejected node identity")
	}
	if err = conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func (s *secureStream) authorized(hello authHello) bool {
	if hello.ClusterID != s.clusterID || hello.NodeID == "" || hello.NodeID == s.localID || len(hello.NodeKey) < 32 {
		return false
	}
	s.mu.RLock()
	peer, ok := s.peersByID[hello.NodeID]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	sum := sha256.Sum256([]byte(hello.NodeKey))
	want, err := hex.DecodeString(peer.NodeKeyHash)
	return err == nil && hmac.Equal(sum[:], want)
}

func (s *secureStream) Close() error   { return s.listener.Close() }
func (s *secureStream) Addr() net.Addr { return s.listener.Addr() }

func writeAuthFrame(w io.Writer, value authHello) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) == 0 || len(data) > maxAuthFrame {
		return errors.New("quorum: peer identity frame exceeds its bound")
	}
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(data)))
	if err := writeAll(w, length[:]); err != nil {
		return err
	}
	return writeAll(w, data)
}

func readAuthFrame(r io.Reader, out *authHello) error {
	var length [4]byte
	if _, err := io.ReadFull(r, length[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(length[:])
	if size == 0 || size > maxAuthFrame {
		return errors.New("quorum: invalid peer identity frame size")
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
