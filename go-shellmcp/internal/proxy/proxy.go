// Package proxy provides a small unauthenticated LAN proxy for a trusted
// tethering link. It intentionally supports TCP CONNECT only: adb forward is
// a TCP transport, so advertising SOCKS5 UDP would be misleading.
package proxy

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// DialFunc creates the outbound connection used by a proxy request.
type DialFunc func(network, address string) (net.Conn, error)

// Handle serves one HTTP CONNECT or SOCKS5 TCP CONNECT connection.
func Handle(conn net.Conn, dial DialFunc) error {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	first, err := reader.Peek(1)
	if err != nil {
		return err
	}
	if first[0] == 5 {
		return handleSOCKS5(conn, reader, dial)
	}
	return handleHTTP(conn, reader, dial)
}

// Serve accepts proxy connections until ctx is cancelled using the system DNS.
func Serve(ctx context.Context, address string) error {
	return ServeWithDNS(ctx, address, "")
}

// ServeWithDNS accepts proxy connections and optionally resolves names through
// the supplied DNS server. Android shell processes can have a working 4G route
// but no visible libc resolver configuration, so an explicit DNS fallback is
// needed for domain-based SOCKS5 and HTTP CONNECT requests.
func ServeWithDNS(ctx context.Context, address, dnsServer string) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	dialer := net.Dialer{}
	if dnsServer != "" {
		dialer.Resolver = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "udp", dnsServer)
			},
		}
	}
	for {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			if ctx.Err() != nil {
				return nil
			}
			return acceptErr
		}
		go func() {
			_ = Handle(conn, func(network, address string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, address)
			})
		}()
	}
}

func handleHTTP(conn net.Conn, reader *bufio.Reader, dial DialFunc) error {
	request, err := http.ReadRequest(reader)
	if err != nil {
		return err
	}
	if request.Method != http.MethodConnect {
		writeHTTPError(conn, http.StatusMethodNotAllowed, "only CONNECT is supported")
		return nil
	}
	address := request.Host
	if !strings.Contains(address, ":") {
		address += ":443"
	}
	upstream, err := dial("tcp", address)
	if err != nil {
		writeHTTPError(conn, http.StatusBadGateway, "upstream dial failed")
		return err
	}
	defer upstream.Close()
	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\nProxy-Agent: gptadmin-android4g\r\n\r\n"); err != nil {
		return err
	}
	return relay(conn, reader, upstream)
}

func handleSOCKS5(conn net.Conn, reader *bufio.Reader, dial DialFunc) error {
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil {
		return err
	}
	if header[0] != 5 {
		return fmt.Errorf("unsupported SOCKS version %d", header[0])
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(reader, methods); err != nil {
		return err
	}
	noAuth := false
	for _, method := range methods {
		if method == 0 {
			noAuth = true
			break
		}
	}
	if noAuth {
		if _, err := conn.Write([]byte{5, 0}); err != nil {
			return err
		}
	} else {
		_, _ = conn.Write([]byte{5, 0xff})
		return fmt.Errorf("SOCKS5 authentication is not supported")
	}

	requestHeader := make([]byte, 4)
	if _, err := io.ReadFull(reader, requestHeader); err != nil {
		return err
	}
	if requestHeader[0] != 5 {
		return fmt.Errorf("unsupported SOCKS version %d", requestHeader[0])
	}
	if requestHeader[1] != 1 {
		_, _ = conn.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
		return fmt.Errorf("SOCKS5 command %d is not supported", requestHeader[1])
	}

	address, err := readSOCKSAddress(reader, requestHeader[3])
	if err != nil {
		_, _ = conn.Write([]byte{5, 8, 0, 1, 0, 0, 0, 0, 0, 0})
		return err
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBytes); err != nil {
		return err
	}
	address += ":" + strconv.Itoa(int(binary.BigEndian.Uint16(portBytes)))
	upstream, err := dial("tcp", address)
	if err != nil {
		_, _ = conn.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return err
	}
	defer upstream.Close()
	if _, err := conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return err
	}
	return relay(conn, reader, upstream)
}

func readSOCKSAddress(reader *bufio.Reader, addressType byte) (string, error) {
	switch addressType {
	case 1:
		value := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(reader, value); err != nil {
			return "", err
		}
		return net.IP(value).String(), nil
	case 3:
		length, err := reader.ReadByte()
		if err != nil {
			return "", err
		}
		value := make([]byte, int(length))
		if _, err := io.ReadFull(reader, value); err != nil {
			return "", err
		}
		return string(value), nil
	case 4:
		value := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(reader, value); err != nil {
			return "", err
		}
		return "[" + net.IP(value).String() + "]", nil
	default:
		return "", fmt.Errorf("unsupported SOCKS5 address type %d", addressType)
	}
}

func relay(client net.Conn, clientReader io.Reader, upstream net.Conn) error {
	errors := make(chan error, 2)
	go func() {
		_, err := io.Copy(upstream, clientReader)
		errors <- err
	}()
	go func() {
		_, err := io.Copy(client, upstream)
		errors <- err
	}()
	return <-errors
}

func writeHTTPError(conn net.Conn, status int, message string) {
	_, _ = fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", status, http.StatusText(status), len(message), message)
}
