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

const (
	vary            = "Vary"
	acceptEncoding  = "Vary"
	contentEncoding = "Content-Encoding"
	contentLength   = "Content-Length"
)

type brotliResponseWriter struct {
	rw http.ResponseWriter
	bw *brotli.Writer

	statusCode int

	minSize int
	buf     []byte

	compressed bool
	headerSent bool
	seenData   bool
}

func (b *brotliResponseWriter) Header() http.Header {
	return b.rw.Header()
}

func (b *brotliResponseWriter) WriteHeader(code int) {
	b.statusCode = code
}

// TODO: filter content blabla

func (b *brotliResponseWriter) Write(p []byte) (int, error) {
	b.seenData = len(p) > 0

	fmt.Printf("Write(%d) %+v\n", len(p), b)
	if b.compressed {
		// If compressed we assume we have sent headers already
		fmt.Println("Write() compressed")
		return b.bw.Write(p)
	}

	if b.rw.Header().Get(contentEncoding) != "" {
		return b.rw.Write(p)
	}

	if len(b.buf)+len(p) < b.minSize {
		b.buf = append(b.buf, p...)
		fmt.Printf("Write() buffered %d\n", len(b.buf))
		return len(p), nil
	}

	b.compressed = true

	b.rw.Header().Del(contentLength)

	// Ensure to write in the correct order.
	b.rw.Header().Add(vary, acceptEncoding)
	b.rw.Header().Set(contentEncoding, "br")
	b.rw.WriteHeader(b.statusCode)
	b.headerSent = true

	n, err := b.bw.Write(b.buf)
	if err != nil {
		// TODO: double-check all the scenarii from the caller in case of an error
		return 0, err
	}
	if n < len(b.buf) {
		b.buf = b.buf[n:]
		b.buf = append(b.buf, p...)
		return len(p), nil
	}
	b.buf = b.buf[:0]

	fmt.Println("Write() first compressed")

	return b.bw.Write(p)
}

// Flush flushes data to the underlying writer.
// If not enough bytes have been written to determine if we have reached minimum size, this will be ignored.
// If nothing has been written yet, nothing will be flushed.
func (b *brotliResponseWriter) Flush() {
	if !b.seenData {
		// we should only flush if we have ever started compressing,
		// because flushing the bw sends some extra end of compressed stream bytes.
		return
	}

	if b.rw.Header().Get(contentEncoding) != "" {
		if rw, ok := b.rw.(http.Flusher); ok {
			rw.Flush()
		}
		return
	}

	if !b.compressed {
		return
	}

	defer func() {
		b.bw.Flush()

		if rw, ok := b.rw.(http.Flusher); ok {
			rw.Flush()
		}
	}()

	n, err := b.bw.Write(b.buf)
	if err != nil {
		return
	}
	if n < len(b.buf) {
		b.buf = b.buf[n:]
		return
	}
	b.buf = b.buf[:0]
}

func (b *brotliResponseWriter) Close() error {
	fmt.Printf("Close() %+v\n", b)

	if !b.headerSent {
		b.rw.Header().Del(vary)
		if b.compressed {
			b.rw.Header().Add(vary, acceptEncoding)
			b.rw.Header().Set(contentEncoding, "br")
		}
		b.rw.WriteHeader(b.statusCode)
		b.headerSent = true
	}

	if len(b.buf) == 0 {
		// we should only close if we have ever started compressing,
		// because closing the bw sends some extra end of compressed stream bytes.
		if !b.compressed {
			return nil
		}

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

	n, err := b.rw.Write(b.buf)
	if err != nil {
		return err
	}
	if n < len(b.buf) {
		return io.ErrShortWrite
	}
	return nil
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
				rw:         rw,
				bw:         brotli.NewWriterLevel(rw, cfg.Compression),
				minSize:    cfg.MinSize,
				statusCode: http.StatusOK,
			}
			defer brw.Close()

			h.ServeHTTP(brw, r)
		}
	}
}

// AcceptsBr is a naive method to check whether brotli is an accepted encoding.
func AcceptsBr(acceptEncoding string) bool {
	for _, v := range strings.Split(acceptEncoding, ",") {
		encodings := strings.Split(strings.TrimSpace(v), ";")
		if len(encodings) == 0 {
			continue
		}
		if encodings[0] == "br" || encodings[0] == "*" {
			return true
		}
	}

	return false
}
