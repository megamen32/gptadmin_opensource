// Dispatcher maps method names from incoming native-messaging requests to
// their handler functions.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
)

// Handler is the signature for all native-messaging method handlers.
type Handler func(ctx context.Context, params json.RawMessage) (interface{}, error)

// handlers maps method names to their handler functions.
var handlers = map[string]Handler{
	"files.list":     handleFilesList,
	"files.read":     handleFilesRead,
	"files.write":    handleFilesWrite,
	"files.watch":    handleFilesWatch,
	"processes.list": handleProcessesList,
	"processes.exec": handleProcessesExec,
	"processes.kill": handleProcessesKill,
	"system.info":    handleSystemInfo,
	"network.dns":    handleNetworkDNS,
}

// Dispatch looks up the handler for the given method and invokes it.
// If the method is not found, it returns a "method not found" error.
func Dispatch(ctx context.Context, method string, params json.RawMessage) (interface{}, error) {
	handler, ok := handlers[method]
	if !ok {
		return nil, fmt.Errorf("unknown method: %q", method)
	}
	return handler(ctx, params)
}

// DispatchMessage is a convenience wrapper that takes a full Message,
// dispatches it, and returns a response Message ready to send.
func DispatchMessage(ctx context.Context, msg *Message) *Message {
	result, err := Dispatch(ctx, msg.Method, msg.Params)

	resp := &Message{ID: msg.ID}

	if err != nil {
		log.Printf("[%s] error: %v", msg.Method, err)
		resp.Error = NewError(ErrCodeInternal, err.Error())
		return resp
	}

	resp.Result = result
	return resp
}
