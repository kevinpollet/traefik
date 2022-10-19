package brotli

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	abrotli "github.com/andybalholm/brotli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/traefik/traefik/v2/pkg/testhelpers"
)

func generateBytes(length int) []byte {
	var value []byte
	for i := 0; i < length; i++ {
		value = append(value, 0x61+byte(i))
	}
	return value
}

func TestNewMiddleware(t *testing.T) {
	defaultMinSize := 10
	testCases := []struct {
		name          string
		writeData     []byte
		writesequence []int
		expCompress   bool
		expEncoding   string
	}{
		{
			name:        "no data to write",
			expCompress: false,
			expEncoding: "identity",
		},
		{
			name:        "big request",
			expCompress: true,
			expEncoding: "br",
			writeData:   generateBytes(defaultMinSize),
		},
		{
			name:        "small request",
			expCompress: false,
			expEncoding: "identity",
			writeData:   generateBytes(defaultMinSize - 1),
		},
		{
			name:          "big request with first small write",
			expCompress:   true,
			expEncoding:   "br",
			writeData:     generateBytes(defaultMinSize * 10),
			writesequence: []int{defaultMinSize - 1},
		},
		{
			name:          "big request with first big write",
			expCompress:   true,
			expEncoding:   "br",
			writeData:     generateBytes(defaultMinSize * 10),
			writesequence: []int{defaultMinSize + 1},
		},
	}

	for _, test := range testCases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			// t.Parallel()

			req := testhelpers.MustNewRequest(http.MethodGet, "http://localhost", nil)

			next := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				sentLength := 0
				for _, length := range test.writesequence {
					_, err := rw.Write(test.writeData[sentLength : sentLength+length])
					require.NoError(t, err)
					sentLength += length
				}

				_, err := rw.Write(test.writeData[sentLength:])
				assert.NoError(t, err)

				rw.WriteHeader(299)
			})

			rw := httptest.NewRecorder()
			NewMiddleware(Config{MinSize: defaultMinSize})(next).ServeHTTP(rw, req)

			assert.Equal(t, test.expEncoding, rw.Header().Get("Content-Encoding"))
			assert.Equal(t, 299, rw.Code, "wrong status code")
			assert.Equal(t, fmt.Sprintf("%d", len(test.writeData)), rw.Header().Get("Content-Length"), "wrong content length")

			if !test.expCompress {
				assert.Equal(t, "", rw.Header().Get("Vary"))
				assert.Equal(t, test.writeData, rw.Body.Bytes())

				return
			}

			assert.Equal(t, "Accept-Encoding", rw.Header().Get("Vary"))

			reader := abrotli.NewReader(rw.Body)
			data, err := io.ReadAll(reader)
			require.NoError(t, err)

			assert.Equal(t, len(test.writeData), len(data))
			assert.Equal(t, test.writeData, data)
		})
	}
}

func TestAcceptsBr(t *testing.T) {
	testCases := []struct {
		name     string
		encoding string
		accepted bool
	}{
		{
			name:     "simple br accept",
			encoding: "br",
			accepted: true,
		},
		{
			name:     "br accept with quality",
			encoding: "br;q=1.0",
			accepted: true,
		},
		{
			name:     "br accept with quality multiple",
			encoding: "gzip;1.0, br;q=0.8",
			accepted: true,
		},
		{
			name:     "any accept with quality multiple",
			encoding: "gzip;q=0.8, *;q=0.1",
			accepted: true,
		},
		{
			name:     "any accept",
			encoding: "*",
			accepted: true,
		},
		{
			name:     "gzip accept",
			encoding: "gzip",
			accepted: false,
		},
		{
			name:     "gzip accept multiple",
			encoding: "gzip, identity",
			accepted: false,
		},
	}

	for _, test := range testCases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.accepted, AcceptsBr(test.encoding))
		})
	}
}
