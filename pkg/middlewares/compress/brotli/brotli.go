package brotli

import (
	"fmt"
	"io"
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
	// TODO: maybe remove
	cl         int
	statusCode int
}

func (b *brotliResponseWriter) WriteHeader(code int) {
	b.statusCode = code
}

func (b *brotliResponseWriter) Write(p []byte) (int, error) {
	fmt.Printf("Write(%d) %+v\n", len(p), b)
	if b.compressed {
		// If compressed we assume we have sent headers already
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
	b.rw.Header().Add("Vary", "Accept-Encoding")
	b.rw.Header().Set("Content-Encoding", "br")
	b.rw.WriteHeader(b.statusCode)
	b.headerSent = true
	n, err := b.bw.Write(b.buf)
	if err != nil {
		// TODO: double-check all the scenarii from the caller in case of an error
		return 0, err
	}
	b.cl += n
	if n < len(b.buf) {
		b.buf = b.buf[n:]
		b.buf = append(b.buf, p...)
		return len(p), nil
	}
	b.buf = b.buf[:0]

	fmt.Println("Write() first compressed")

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
		return b.bw.Close()
	}

	fmt.Println("Close() flushing")

	if b.compressed {
		n, err := b.bw.Write(b.buf)
		if err != nil {
			b.bw.Close()
			return err
		}
		if n < len(b.buf) {
			b.bw.Close()
			return io.ErrShortWrite
		}
		return b.bw.Close()
	}

	b.rw.Header().Del("Vary")
	// TODO: do not override if it was already set (because previously compressed)
	// TODO: and it might decide whether we actually compress or not
	b.rw.Header().Set("Content-Encoding", "identity")
	b.rw.WriteHeader(b.statusCode)
	b.headerSent = true

	n, err := b.rw.Write(b.buf)
	if err != nil {
		b.bw.Close()
		return err
	}
	if n < len(b.buf) {
		b.bw.Close()
		return io.ErrShortWrite
	}
	return b.bw.Close()
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
