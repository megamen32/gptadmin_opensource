// Package main implements a Chrome native messaging host for GPTAdmin Cloud OS.
//
// Chrome native messaging uses a simple protocol: each message is prefixed
// with a 4-byte little-endian unsigned 32-bit integer indicating the
// message length in bytes, followed by the JSON-encoded message body.
package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// Message is the native messaging envelope.
// Chrome sends requests like: {"id": "req-123", "method": "files.list", "params": {"path": "/Users/me"}}
// We respond with:     {"id": "req-123", "result": {...}} or {"id": "req-123", "error": {...}}
type Message struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
	Result interface{}     `json:"result,omitempty"`
	Error  *ErrorObj       `json:"error,omitempty"`
}

// ErrorObj represents a structured error returned in the response.
type ErrorObj struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Error codes following a JSON-RPC–inspired convention.
const (
	ErrCodeInternal    = -32603 // Internal error
	ErrCodeInvalidArgs = -32602 // Invalid parameters
	ErrCodeNotImpl     = -32601 // Method not implemented
	ErrCodeForbidden   = -32600 // Security / access denied
)

// NewError creates an ErrorObj with the given code and message.
func NewError(code int, message string) *ErrorObj {
	return &ErrorObj{Code: code, Message: message}
}

// NewInternalError is a convenience constructor for internal errors.
func NewInternalError(message string) *ErrorObj {
	return NewError(ErrCodeInternal, message)
}

// ReadMessage reads a single native-messaging message from r.
// It first reads a 4-byte little-endian length prefix, then reads
// exactly that many bytes and unmarshals them as JSON.
func ReadMessage(r io.Reader) (*Message, error) {
	// Read the 4-byte length prefix.
	var length uint32
	if err := binary.Read(r, binary.LittleEndian, &length); err != nil {
		return nil, fmt.Errorf("reading message length: %w", err)
	}

	// Guard against unreasonably large messages (256 MB ceiling).
	const maxLen = 256 * 1024 * 1024
	if length > maxLen {
		return nil, fmt.Errorf("message length %d exceeds maximum %d", length, maxLen)
	}

	// Read the JSON payload.
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("reading message body: %w", err)
	}

	var msg Message
	if err := json.Unmarshal(buf, &msg); err != nil {
		return nil, fmt.Errorf("unmarshaling message: %w", err)
	}

	return &msg, nil
}

// WriteMessage writes a single native-messaging message to w.
// It marshals the message as JSON, writes a 4-byte little-endian
// length prefix, then writes the JSON bytes.
func WriteMessage(w io.Writer, msg *Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshaling response: %w", err)
	}

	// Write the 4-byte length prefix.
	length := uint32(len(data))
	if err := binary.Write(w, binary.LittleEndian, length); err != nil {
		return fmt.Errorf("writing message length: %w", err)
	}

	// Write the JSON body.
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("writing message body: %w", err)
	}

	return nil
}
