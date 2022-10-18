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
}

func (b *brotliResponseWriter) WriteHeader(code int) {
	return
	fmt.Printf("brotliResponseWriter.WriteHeader(%d)\n", code)
	if b.headerSent {
		return
	}

	b.headerSent = true

	if b.compressed {
		b.rw.Header().Add("Vary", "Accept-Encoding")
		b.rw.Header().Set("Content-Encoding", "br")
		b.rw.WriteHeader(code)

		return
	}

	b.rw.Header().Del("Vary")
	b.rw.Header().Set("Content-Encoding", "identity")
	// TODO "Content-Length"
	b.rw.WriteHeader(code)
}

func (b *brotliResponseWriter) Write(p []byte) (int, error) {
	fmt.Printf("brotliResponseWriter.Write(%d) %+v\n", len(p), b)
	if b.compressed {
		if !b.headerSent {
			b.rw.Header().Add("Vary", "Accept-Encoding")
			b.rw.Header().Set("Content-Encoding", "br")
			b.headerSent = true
		}

		fmt.Println("brotliResponseWriter.Write() compressed")
		return b.bw.Write(p)
	}

	if len(b.buf)+len(p) < b.minSize {
		b.buf = append(b.buf, p...)
		fmt.Printf("brotliResponseWriter.Write() buffered %d\n", len(b.buf))
		return len(p), nil
	}

	b.compressed = true

	if !b.headerSent {
		b.rw.Header().Add("Vary", "Accept-Encoding")
		b.rw.Header().Set("Content-Encoding", "br")
		b.headerSent = true
	}

	return b.bw.Write(p)
}

func (b *brotliResponseWriter) Header() http.Header {
	return b.rw.Header()
}

func (b *brotliResponseWriter) Close() error {
	fmt.Println("brotliResponseWriter.Close()")
	if len(b.buf) == 0 {
		return nil
	}

	if b.compressed {
		// TODO Check if closer ?
		_, err := b.bw.Write(b.buf)
		return err
	}

	if !b.headerSent {
		b.rw.Header().Del("Vary")
		b.rw.Header().Set("Content-Encoding", "identity")
		b.headerSent = true
	}

	_, err := b.rw.Write(b.buf)
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
