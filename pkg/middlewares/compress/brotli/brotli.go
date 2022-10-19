package brotli

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/andybalholm/brotli"
)

// DefaultMinSize is te default minimum size until we enable brotli
// compression.
// 1500 bytes is the MTU size for the internet since that is the largest size
// allowed at the network layer. If you take a file that is 1300 bytes and
// compress it to 800 bytes, it’s still transmitted in that same 1500 byte
// packet regardless, so you’ve gained nothing. That being the case, you should
// restrict the gzip compression to files with a size (plus header) greater
// than a single packet, 1024 bytes (1KB) is therefore default.
// From [github.com/klauspost/compress/gzhttp](https://github.com/klauspost/compress/tree/master/gzhttp).
var DefaultMinSize = 1024

// TODO Flusher ?
type brotliResponseWriter struct {
	rw         http.ResponseWriter
	bw         *brotli.Writer
	minSize    int
	buf        []byte
	compressed bool
	headerSent bool
	cl         int
}

func (b *brotliResponseWriter) WriteHeader(code int) {
	fmt.Printf("WriteHeader(%d) headerSent: %t cl: %d\n", code, b.headerSent, b.cl+len(b.buf))

	if b.headerSent {
		return
	}

	// b.rw.Header().Set("Content-Length", fmt.Sprintf("%d", b.cl+len(b.buf)))

	b.rw.WriteHeader(code)

	b.headerSent = true

	if b.compressed {
		b.rw.Header().Add("Vary", "Accept-Encoding")
		b.rw.Header().Set("Content-Encoding", "br")

		return
	}

	b.rw.Header().Del("Vary")
	b.rw.Header().Set("Content-Encoding", "identity")
}

func (b *brotliResponseWriter) Write(p []byte) (int, error) {
	fmt.Printf("Write(%d) %+v\n", len(p), b)
	if b.compressed {
		if !b.headerSent {
			b.rw.Header().Add("Vary", "Accept-Encoding")
			b.rw.Header().Set("Content-Encoding", "br")
			b.headerSent = true
		}

		fmt.Println("Write() compressed")
		n, err := b.bw.Write(p)
		b.cl += n
		return n, err
	}

	if len(b.buf)+len(p) < b.minSize {
		b.buf = append(b.buf, p...)
		fmt.Printf("Write() buffered %d\n", len(b.buf))
		return len(p), nil
	}

	b.compressed = true

	b.rw.Header().Del("Content-Length")

	// Ensure to write in the correct order.
	n, err := b.bw.Write(b.buf)
	if err != nil {
		return n, err
	}
	b.cl += n
	b.buf = nil

	if !b.headerSent {
		b.rw.Header().Add("Vary", "Accept-Encoding")
		b.rw.Header().Set("Content-Encoding", "br")
		b.headerSent = true
	}

	fmt.Println("Write() first compressed")

	b.rw.WriteHeader(299) // FIXME

	n, err = b.bw.Write(p)
	b.cl += n
	return n, err
}

func (b *brotliResponseWriter) Header() http.Header {
	return b.rw.Header()
}

func (b *brotliResponseWriter) Close() error {
	fmt.Printf("Close() %+v\n", b)
	if len(b.buf) == 0 {
		return nil
	}

	fmt.Println("Close() flushing")

	if b.compressed {
		// TODO Check if closer ?
		n, err := b.bw.Write(b.buf)
		if err != nil {
			return err
		}

		b.cl += n

		return b.bw.Close()
	}

	if !b.headerSent {
		b.rw.Header().Del("Vary")
		b.rw.Header().Set("Content-Encoding", "identity")
		b.headerSent = true
	}

	n, err := b.rw.Write(b.buf)
	b.cl += n
	return err
}

// Config is the brotli middleware configuration.
type Config struct {
	// Compression level.
	Compression int
	// MinSize is the minimum size until we enable brotli compression.
	MinSize int
}

// NewMiddleware returns a new brotli compressing middleware.
func NewMiddleware(cfg Config) func(http.Handler) http.HandlerFunc {
	return func(h http.Handler) http.HandlerFunc {
		return func(rw http.ResponseWriter, r *http.Request) {
			brw := &brotliResponseWriter{
				rw:      rw,
				bw:      brotli.NewWriterLevel(rw, cfg.Compression),
				minSize: cfg.MinSize,
			}
			defer brw.Close()

			h.ServeHTTP(brw, r)
		}
	}
}

// AcceptsBr is a naive method to check whether brotli is an accepted encoding.
func AcceptsBr(acceptEncoding string) bool {
	for _, v := range strings.Split(acceptEncoding, ",") {
		for i, e := range strings.Split(strings.TrimSpace(v), ";") {
			if i == 0 && (e == "br" || e == "*") {
				return true
			}

			break
		}
	}

	return false
}
