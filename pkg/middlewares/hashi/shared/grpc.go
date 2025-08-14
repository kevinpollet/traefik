// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package shared

import (
	"context"

	"github.com/traefik/traefik/v3/pkg/middlewares/hashi/proto"
)

type GRPCClient struct {
	client proto.MiddlewareClient
}

func (m *GRPCClient) Process(ctx context.Context, req *proto.Request) (*proto.Response, error) {
	resp, err := m.client.Process(ctx, req)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

type GRPCServer struct {
	// This is the real implementation
	Impl MiddlewarePlugin
}

func (m *GRPCServer) Process(_ context.Context, req *proto.Request) (*proto.Response, error) {
	return m.Impl.Process(req)
}
