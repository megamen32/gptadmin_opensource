// Package runtime embeds the existing executor in a GPTAdmin node. Legacy
// standalone shellmcp keeps its existing entrypoint and configuration.
package runtime

import "github.com/megamen32/gptadmin/go-shellmcp/internal/server"

type Config = server.Config
type Server = server.Server

func FromEnv() Config        { return server.FromEnv() }
func New(cfg Config) *Server { return server.New(cfg) }
