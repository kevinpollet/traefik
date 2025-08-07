// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package shared

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/go-plugin"
	"github.com/traefik/traefik/v3/pkg/middlewares/hashi/proto"
	"google.golang.org/grpc"
)

type GRPCClient struct {
	client proto.MiddlewarePluginClient
	broker *plugin.GRPCBroker
}

func (m *GRPCClient) HandleRequest(req *http.Request, modifier RequestModifier) error {
	modifierServer := &GRPCRequestModifierServer{Impl: modifier}

	var s *grpc.Server
	serverFunc := func(opts []grpc.ServerOption) *grpc.Server {
		s = grpc.NewServer(opts...)
		proto.RegisterRequestModifierServer(s, modifierServer)
		return s
	}

	brokerID := m.broker.NextId()
	go m.broker.AcceptAndServe(brokerID, serverFunc)

	headers := make(map[string]*proto.HeaderValues, len(req.Header))
	for k, v := range req.Header {
		headers[k] = &proto.HeaderValues{Values: v}
	}

	_, err := m.client.HandleRequest(context.Background(), &proto.Request{
		RequestModifierServer: brokerID,
		Method:                req.Method,
		Path:                  req.URL.Path,
		Headers:               headers,
	})

	s.Stop()
	return err
}

type GRPCServer struct {
	// This is the real implementation
	Impl   MiddlewarePlugin
	broker *plugin.GRPCBroker
}

func (m *GRPCServer) HandleRequest(ctx context.Context, req *proto.Request) (*proto.Empty, error) {
	conn, err := m.broker.Dial(req.RequestModifierServer)
	if err != nil {
		return nil, fmt.Errorf("dialing to request modifier: %w", err)
	}
	defer func() { _ = conn.Close() }()

	r := &GRPCRequestModifierClient{proto.NewRequestModifierClient(conn)}
	return &proto.Empty{}, m.Impl.HandleRequest(req, r)
}

type GRPCRequestModifierClient struct{ client proto.RequestModifierClient }

func (m *GRPCRequestModifierClient) HeaderAdd(key, value string) error {
	_, err := m.client.HeaderAdd(context.Background(), &proto.HeaderRequest{
		Key:   key,
		Value: value,
	})
	if err != nil {
		return fmt.Errorf("adding header: %w", err)
	}
	return nil
}

func (m *GRPCRequestModifierClient) HeaderSet(key, value string) error {
	_, err := m.client.HeaderSet(context.Background(), &proto.HeaderRequest{
		Key:   key,
		Value: value,
	})
	if err != nil {
		return fmt.Errorf("setting header: %w", err)
	}
	return nil
}

func (m *GRPCRequestModifierClient) HeaderDel(key string) error {
	_, err := m.client.HeaderDel(context.Background(), &proto.HeaderRequest{Key: key})
	if err != nil {
		return fmt.Errorf("deleting header: %w", err)
	}
	return nil
}

type GRPCRequestModifierServer struct {
	// This is the real implementation
	Impl RequestModifier
}

func (m *GRPCRequestModifierServer) HeaderAdd(ctx context.Context, req *proto.HeaderRequest) (*proto.Empty, error) {
	if err := m.Impl.HeaderAdd(req.Key, req.Value); err != nil {
		return nil, fmt.Errorf("adding header: %w", err)
	}
	return &proto.Empty{}, nil
}

func (m *GRPCRequestModifierServer) HeaderSet(ctx context.Context, req *proto.HeaderRequest) (*proto.Empty, error) {
	if err := m.Impl.HeaderSet(req.Key, req.Value); err != nil {
		return nil, fmt.Errorf("setting header: %w", err)
	}
	return &proto.Empty{}, nil
}

func (m *GRPCRequestModifierServer) HeaderDel(ctx context.Context, req *proto.HeaderRequest) (*proto.Empty, error) {
	if err := m.Impl.HeaderDel(req.Key); err != nil {
		return nil, fmt.Errorf("deleting header: %w", err)
	}
	return &proto.Empty{}, nil
}
