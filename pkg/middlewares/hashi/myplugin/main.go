package main

import (
	"github.com/hashicorp/go-plugin"
	"github.com/traefik/traefik/v3/pkg/middlewares/hashi/proto"
	"github.com/traefik/traefik/v3/pkg/middlewares/hashi/shared"
)

type MyPlugin struct{}

func (MyPlugin) HandleRequest(req *proto.Request) (*proto.Response, error) {
	//println("Received request:", req.Method, req.Path, req.Headers)
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
			"myplugin": &shared.GRPCMiddlewarePlugin{Impl: &MyPlugin{}},
		},
		// A non-nil value here enables gRPC serving for this plugin...
		GRPCServer: plugin.DefaultGRPCServer,
	})
}
