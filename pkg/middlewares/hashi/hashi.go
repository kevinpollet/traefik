package hashi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/hashicorp/go-plugin"
	"github.com/traefik/traefik/v3/pkg/middlewares"
	"github.com/traefik/traefik/v3/pkg/middlewares/hashi/shared"
)

const typeName = "Hashi"

// FIXME: This simulates having only one client by plugin and each middleware will use it.
// This would likely be done when Traefik starts and loads the plugin.
// As stated in here:https://discuss.hashicorp.com/t/go-plugin-concurrency/1669 plugin client are meant to be reused and are goroutine-safe.
var client plugin.ClientProtocol
var startPlugin = sync.OnceFunc(func() {
	var err error
	//path := "/Users/kevinpollet/Documents/Dev/traefik/pkg/middlewares/hashi/myplugin"

	// FIXME this should be commented to run with go run (for perf env).
	path, err := os.Executable()
	if err != nil {
		panic(err)
	}
	path = filepath.Dir(path)

	c := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig:  shared.Handshake,
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
		Managed:          true,
		Plugins: map[string]plugin.Plugin{
			"myplugin": &shared.GRPCMiddlewarePlugin{},
		},
		Cmd: exec.Command("sh", "-c", path+"/myplugin"), //  This could be injected by configuration.
	})

	client, err = c.Client()
	if err != nil {
		panic(err) // FIXME
	}
})

type Hashi struct {
	name   string
	plugin *shared.GRPCClient

	next http.Handler
}

func New(ctx context.Context, next http.Handler, config struct{}, name string) (*Hashi, error) {
	logger := middlewares.GetLogger(ctx, name, typeName)
	logger.Debug().Msg("Creating middleware")

	// FIXME: called only once.
	startPlugin()

	// Request the plugin
	raw, err := client.Dispense("myplugin")
	if err != nil {
		return nil, fmt.Errorf("dispening RPC plugin: %w", err)
	}

	// We should have a MiddlewarePlugin store now!
	// This feels like a normal interface implementation but is in fact over an RPC connection.
	p, ok := raw.(*shared.GRPCClient)
	if !ok {
		return nil, fmt.Errorf("expected middlewarePlugin, got %T", raw)
	}

	return &Hashi{
		name:   name,
		plugin: p,
		next:   next,
	}, nil
}

func (h *Hashi) GetTracingInformation() (string, string) {
	return h.name, typeName
}

func (h *Hashi) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	var envoyHeaders []*corev3.HeaderValue
	for k, v := range req.Header {
		for _, vv := range v {
			envoyHeaders = append(envoyHeaders, &corev3.HeaderValue{
				Key:   k,
				Value: vv,
			})
		}
	}

	resp, err := h.plugin.Process(req.Context(), &extprocv3.ProcessingRequest{
		Request: &extprocv3.ProcessingRequest_RequestHeaders{
			RequestHeaders: &extprocv3.HttpHeaders{
				Headers:     &corev3.HeaderMap{Headers: envoyHeaders},
				EndOfStream: req.ContentLength == 0,
			},
		},
	})
	if err != nil {
		return
	}

	if resp.GetRequestHeaders() != nil && resp.GetRequestHeaders().GetResponse() != nil && resp.GetRequestHeaders().GetResponse().GetHeaderMutation() != nil {
		mutations := resp.GetRequestHeaders().GetResponse().GetHeaderMutation()
		for _, header := range mutations.SetHeaders {
			req.Header.Set(header.GetHeader().Key, header.GetHeader().Value)
		}
	}

	h.next.ServeHTTP(rw, req)
}
