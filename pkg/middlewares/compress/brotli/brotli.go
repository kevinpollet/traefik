package brotli

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/andybalholm/brotli"
)

const (
	vary            = "Vary"
	acceptEncoding  = "Accept-Encoding"
	contentEncoding = "Content-Encoding"
	contentLength   = "Content-Length"
	contentType     = "Content-Type"
)

type brotliResponseWriter struct {
	rw http.ResponseWriter
	bw *brotli.Writer

	minSize              int
	excludedContentTypes []parsedContentType

	buf                []byte
	statusCode         int
	skipCompression    bool
	compressionStarted bool
	headersSent        bool
	seenData           bool
}

func (b *brotliResponseWriter) Header() http.Header {
	return b.rw.Header()
}

func (b *brotliResponseWriter) WriteHeader(code int) {
	b.statusCode = code
}

func (b *brotliResponseWriter) Write(p []byte) (int, error) {
	if !b.seenData && len(p) > 0 {
		b.seenData = true
	}

	if b.skipCompression {
		return b.rw.Write(p)
	}

	fmt.Printf("Write(%d) %+v\n", len(p), b)
	if b.compressionStarted {
		// If compressionStarted we assume we have sent headers already
		fmt.Println("Write() compressionStarted")
		return b.bw.Write(p)
	}

	if b.rw.Header().Get(contentEncoding) != "" {
		b.skipCompression = true
		return b.rw.Write(p)
	}

	if ct := b.rw.Header().Get(contentType); ct != "" {
		mediaType, params, err := mime.ParseMediaType(ct)
		if err != nil {
			return 0, fmt.Errorf("unable to parse media type: %w", err)
		}

		for _, excludedContentType := range b.excludedContentTypes {
			if excludedContentType.equals(mediaType, params) {
				b.skipCompression = true
				return b.rw.Write(p)
			}
		}
	}

	if len(b.buf)+len(p) < b.minSize {
		b.buf = append(b.buf, p...)
		fmt.Printf("Write() buffered %d\n", len(b.buf))
		return len(p), nil
	}

	b.compressionStarted = true

	b.rw.Header().Del(contentLength)

	// Ensure to write in the correct order.
	b.rw.Header().Add(vary, acceptEncoding)
	b.rw.Header().Set(contentEncoding, "br")
	b.rw.WriteHeader(b.statusCode)
	b.headersSent = true

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

	fmt.Println("Write() first compressionStarted")

	return b.bw.Write(p)
}

// Flush flushes data to the underlying writer.
// If not enough bytes have been written to determine if we have reached minimum size, this will be ignored.
// If nothing has been written yet, nothing will be flushed.
func (b *brotliResponseWriter) Flush() {
	if !b.headersSent {
		b.rw.WriteHeader(b.statusCode)
		b.headersSent = true
	}

	if !b.seenData {
		// we should only flush if we have ever started compressing,
		// because flushing the bw sends some extra end of compressionStarted stream bytes.
		return
	}

	if b.skipCompression {
		if rw, ok := b.rw.(http.Flusher); ok {
			rw.Flush()
		}
		return
	}

	if b.rw.Header().Get(contentEncoding) != "" {
		b.skipCompression = true
		if rw, ok := b.rw.(http.Flusher); ok {
			rw.Flush()
		}
		return
	}

	if ct := b.rw.Header().Get(contentType); ct != "" {
		mediaType, params, err := mime.ParseMediaType(ct)
		if err != nil {
			return
		}

		for _, excludedContentType := range b.excludedContentTypes {
			if excludedContentType.equals(mediaType, params) {
				b.skipCompression = true
				if rw, ok := b.rw.(http.Flusher); ok {
					rw.Flush()
				}
				return
			}
		}
	}

	if !b.compressionStarted {
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

	if !b.headersSent {
		b.rw.WriteHeader(b.statusCode)
		b.headersSent = true
	}

	if len(b.buf) == 0 {
		// we should only close if we have ever started compressing,
		// because closing the bw sends some extra end of compressionStarted stream bytes.
		if !b.compressionStarted {
			return nil
		}

		return b.bw.Close()
	}

	fmt.Println("Close() flushing")

	if b.compressionStarted {
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
	// ExcludedContentTypes specifies a list of content types to compare
	// to the Content-Type header before compressing.
	// If none match, the response will not be compressionStarted.
	ExcludedContentTypes []string
}

// NewMiddleware returns a new brotli compressing middleware.
func NewMiddleware(cfg Config) func(http.Handler) http.HandlerFunc {
	return func(h http.Handler) http.HandlerFunc {
		return func(rw http.ResponseWriter, r *http.Request) {
			var excludedContentTypes []parsedContentType
			for _, v := range cfg.ExcludedContentTypes {
				mediaType, params, err := mime.ParseMediaType(v)
				if err == nil {
					excludedContentTypes = append(excludedContentTypes, parsedContentType{mediaType, params})
				}
			}

			brw := &brotliResponseWriter{
				rw:                   rw,
				bw:                   brotli.NewWriterLevel(rw, cfg.Compression),
				minSize:              cfg.MinSize,
				statusCode:           http.StatusOK,
				excludedContentTypes: excludedContentTypes,
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
