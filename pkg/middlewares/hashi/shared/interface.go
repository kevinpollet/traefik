// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

// Package shared contains shared data between the host and plugins.
package shared

import (
	"context"

	"github.com/hashicorp/go-plugin"
	"github.com/traefik/traefik/v3/pkg/middlewares/hashi/proto"
	"google.golang.org/grpc"
)

// Handshake is a common handshake that is shared by plugin and host.
var Handshake = plugin.HandshakeConfig{
	// This isn't required when using VersionedPlugins
	ProtocolVersion:  1,
	MagicCookieKey:   "BASIC_PLUGIN",
	MagicCookieValue: "hello",
}

// MiddlewarePlugin is the interface that we're exposing as a plugin.
type MiddlewarePlugin interface {
	Process(req *proto.Request) (*proto.Response, error)
}

type GRPCMiddlewarePlugin struct {
	// GRPCPlugin must still implement the Plugin interface
	plugin.Plugin
	// Concrete implementation, written in Go. This is only used for plugins
	// that are written in Go.
	Impl MiddlewarePlugin
}

func (p *GRPCMiddlewarePlugin) GRPCServer(broker *plugin.GRPCBroker, s *grpc.Server) error {
	proto.RegisterMiddlewareServer(s, &GRPCServer{
		Impl: p.Impl,
	})
	return nil
}

func (p *GRPCMiddlewarePlugin) GRPCClient(_ context.Context, broker *plugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
	return &GRPCClient{
		client: proto.NewMiddlewareClient(c),
	}, nil
}
