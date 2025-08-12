// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package shared

import (
	"context"
	"fmt"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
)

type GRPCClient struct {
	client extprocv3.ExternalProcessorClient
}

func (m *GRPCClient) Process(ctx context.Context, req *extprocv3.ProcessingRequest) (*extprocv3.ProcessingResponse, error) {
	c, err := m.client.Process(ctx)
	if err != nil {
		return nil, err
	}

	if err := c.Send(req); err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}

	resp, err := c.Recv()
	if err != nil {
		return nil, fmt.Errorf("receiving response: %w", err)
	}

	return resp, nil
}

type GRPCServer struct {
	extprocv3.UnimplementedExternalProcessorServer

	// This is the real implementation
	Impl MiddlewarePlugin
}

func (m *GRPCServer) Process(req extprocv3.ExternalProcessor_ProcessServer) error {
	r, err := req.Recv()
	if err != nil {
		return fmt.Errorf("receiving request: %w", err)
	}

	resp, err := m.Impl.Process(r)
	if err != nil {
		return fmt.Errorf("processing request: %w", err)
	}

	if err := req.Send(resp); err != nil {
		return fmt.Errorf("sending response: %w", err)
	}

	return nil
}
