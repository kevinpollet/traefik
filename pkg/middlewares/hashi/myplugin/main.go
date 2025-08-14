package main

import (
	"github.com/hashicorp/go-plugin"
	"github.com/traefik/myplugin/proto"
	"github.com/traefik/myplugin/shared"
)

type MyPlugin struct{}

func (MyPlugin) Process(_ *proto.Request) (*proto.Response, error) {
	return &proto.Response{
		SetHeaders: map[string]string{
			"X-Plugin": "hello",
		},
	}, nil
}

func main() {
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: shared.Handshake,
		Plugins: map[string]plugin.Plugin{
			"myplugin": &shared.GRPCMiddlewarePlugin{
				Impl: MyPlugin{},
			},
		},
		// A non-nil value here enables gRPC serving for this plugin...
		GRPCServer: plugin.DefaultGRPCServer,
	})
}
