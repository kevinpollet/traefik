package compress

import (
	"compress/gzip"
	"context"
	"mime"
	"net/http"
	"strings"

	abbrotli "github.com/andybalholm/brotli"
	"github.com/klauspost/compress/gzhttp"
	"github.com/opentracing/opentracing-go/ext"
	"github.com/traefik/traefik/v2/pkg/config/dynamic"
	"github.com/traefik/traefik/v2/pkg/log"
	"github.com/traefik/traefik/v2/pkg/middlewares"
	"github.com/traefik/traefik/v2/pkg/middlewares/compress/brotli"
	"github.com/traefik/traefik/v2/pkg/tracing"
)

const typeName = "Compress"

// DefaultMinSize is te default minimum size until we enable brotli
// compression.
// 1500 bytes is the MTU size for the internet since that is the largest size
// allowed at the network layer. If you take a file that is 1300 bytes and
// compress it to 800 bytes, it’s still transmitted in that same 1500 byte
// packet regardless, so you’ve gained nothing. That being the case, you should
// restrict the gzip compression to files with a size (plus header) greater
// than a single packet, 1024 bytes (1KB) is therefore default.
// From [github.com/klauspost/compress/gzhttp](https://github.com/klauspost/compress/tree/master/gzhttp).
const DefaultMinSize = 1024

// Compress is a middleware that allows to compress the response.
type compress struct {
	next          http.Handler
	name          string
	excludes      []string
	minSize       int
	brotliHandler http.Handler
	gzipHandler   http.Handler
}

// New creates a new compress middleware.
func New(ctx context.Context, next http.Handler, conf dynamic.Compress, name string) (http.Handler, error) {
	log.FromContext(middlewares.GetLoggerCtx(ctx, name, typeName)).Debug("Creating middleware")

	excludes := []string{"application/grpc"}
	for _, v := range conf.ExcludedContentTypes {
		mediaType, _, err := mime.ParseMediaType(v)
		if err != nil {
			return nil, err
		}

		excludes = append(excludes, mediaType)
	}

	minSize := DefaultMinSize
	if conf.MinResponseBodyBytes > 0 {
		minSize = conf.MinResponseBodyBytes
	}

	c := &compress{
		next:     next,
		name:     name,
		excludes: excludes,
		minSize:  minSize,
	}

	c.brotliHandler = c.newBrotliHandler()

	var err error
	c.gzipHandler, err = c.newGzipHandler()
	if err != nil {
		return nil, err
	}

	return c, nil
}

func (c *compress) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodHead {
		c.next.ServeHTTP(rw, req)
		return
	}

	ctx := middlewares.GetLoggerCtx(req.Context(), c.name, typeName)
	mediaType, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil {
		log.FromContext(ctx).Debug(err)
	}

	if contains(c.excludes, mediaType) {
		c.next.ServeHTTP(rw, req)
		return
	}

	acceptEncoding := strings.TrimSpace(req.Header.Get("Accept-Encoding"))
	if acceptEncoding == "" {
		c.next.ServeHTTP(rw, req)
		return
	}

	if brotli.AcceptsBr(acceptEncoding) {
		c.brotliHandler.ServeHTTP(rw, req)
		return
	}

	c.gzipHandler.ServeHTTP(rw, req)
}

func (c *compress) GetTracingInformation() (string, ext.SpanKindEnum) {
	return c.name, tracing.SpanKindNoneEnum
}

func (c *compress) newGzipHandler() (http.Handler, error) {
	wrapper, err := gzhttp.NewWrapper(
		gzhttp.ExceptContentTypes(c.excludes),
		gzhttp.CompressionLevel(gzip.DefaultCompression),
		gzhttp.MinSize(c.minSize))
	if err != nil {
		return nil, err
	}

	return wrapper(c.next), nil
}

func (c *compress) newBrotliHandler() http.Handler {
	return brotli.NewMiddleware(
		brotli.Config{
			Compression: abbrotli.DefaultCompression,
			MinSize:     c.minSize,
		},
	)(c.next)
}

func contains(values []string, val string) bool {
	for _, v := range values {
		if v == val {
			return true
		}
	}
	return false
}
