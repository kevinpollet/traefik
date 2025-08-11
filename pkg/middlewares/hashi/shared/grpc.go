// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package shared

import (
	"context"
	"fmt"
	"net/http"

	"github.com/traefik/traefik/v3/pkg/middlewares/hashi/proto"
)

type GRPCClient struct {
	client proto.MiddlewarePluginClient
}

func (m *GRPCClient) HandleRequest(req *http.Request) (*Response, error) {
	headers := make(map[string]*proto.HeaderValues, len(req.Header))
	for k, v := range req.Header {
		headers[k] = &proto.HeaderValues{Values: v}
	}

	resp, err := m.client.HandleRequest(context.Background(), &proto.Request{
		Method:  req.Method,
		Path:    req.URL.Path,
		Headers: headers,
	})
	if err != nil {
		return nil, fmt.Errorf("calling HandleRequest: %w", err)
	}

	return &Response{SetHeaders: resp.SetHeaders}, nil
}

type GRPCServer struct {
	// This is the real implementation
	Impl MiddlewarePlugin
}

func (m *GRPCServer) HandleRequest(ctx context.Context, req *proto.Request) (*proto.Response, error) {
	return m.Impl.HandleRequest(req)
}
