package brotli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	abrotli "github.com/andybalholm/brotli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/traefik/traefik/v2/pkg/testhelpers"
)

func Test_Compress(t *testing.T) {
	defaultMinSize := 10
	testCases := []struct {
		desc        string
		data        []byte
		chunkLength int
		expCompress bool
		expEncoding string
	}{

		// TODO: scenario with no Write at all ?
		{
			desc:        "no data to write",
			expCompress: false,
			expEncoding: "",
		},
		{
			desc:        "big request",
			expCompress: true,
			expEncoding: "br",
			data:        generateBytes(defaultMinSize),
		},
		{
			desc:        "small request",
			expCompress: false,
			expEncoding: "",
			data:        generateBytes(defaultMinSize - 1),
		},
		{
			desc:        "big request with first small write",
			expCompress: true,
			expEncoding: "br",
			data:        generateBytes(defaultMinSize * 10),
			chunkLength: defaultMinSize - 1,
		},
		{
			desc:        "big request with first big write",
			expCompress: true,
			expEncoding: "br",
			data:        generateBytes(defaultMinSize * 10),
			chunkLength: defaultMinSize + 1,
		},
	}

	for _, test := range testCases {
		test := test
		t.Run(test.desc, func(t *testing.T) {
			// t.Parallel()
			if test.desc != "no data to write" {
				// return
			}

			req := testhelpers.MustNewRequest(http.MethodGet, "http://localhost", nil)

			next := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				var start, end int
				for test.chunkLength != 0 {
					if start+test.chunkLength >= len(test.data) {
						end = len(test.data)
					} else {
						end = start + test.chunkLength
					}
					n, err := rw.Write(test.data[start:end])
					require.NoError(t, err)
					start += n
					if start >= len(test.data) {
						return
					}
				}

				var err error
				_, err = rw.Write(test.data)
				assert.NoError(t, err)
			})

			rw := httptest.NewRecorder()
			NewMiddleware(Config{MinSize: defaultMinSize})(next).ServeHTTP(rw, req)

			assert.Equal(t, test.expEncoding, rw.Header().Get("Content-Encoding"))
			// TODO: add parameter: explicit WriteHeader call
			assert.Equal(t, 200, rw.Code, "wrong status code")
			// assert.Equal(t, fmt.Sprintf("%d", len(test.data)), rw.Header().Get("Content-Length"), "wrong content length")

			if !test.expCompress {
				assert.Equal(t, "", rw.Header().Get("Vary"))
				assert.Equal(t, len(test.data), rw.Body.Len())
				if test.data != nil {
					assert.Equal(t, test.data, rw.Body.Bytes())
				}

				return
			}

			assert.Equal(t, "Accept-Encoding", rw.Header().Get("Vary"))

			reader := abrotli.NewReader(rw.Body)
			data, err := io.ReadAll(reader)
			require.NoError(t, err)

			assert.Equal(t, len(test.data), len(data))
			assert.Equal(t, test.data, data)
		})
	}
}

func Test_AcceptsBr(t *testing.T) {
	testCases := []struct {
		desc        string
		reqEncoding string
		accepted    bool
	}{
		{
			desc:        "br requested, br accepted",
			reqEncoding: "br",
			accepted:    true,
		},
		{
			desc:        "gzip requested, br not accepted",
			reqEncoding: "gzip",
			accepted:    false,
		},
		{
			desc:        "any requested, br accepted",
			reqEncoding: "*",
			accepted:    true,
		},
		{
			desc:        "gzip and br requested, br accepted",
			reqEncoding: "gzip, br",
			accepted:    true,
		},
		{
			desc:        "gzip and any requested, br accepted",
			reqEncoding: "gzip, *",
			accepted:    true,
		},
		{
			desc:        "gzip and identity requested, br not accepted",
			reqEncoding: "gzip, identity",
			accepted:    false,
		},
	}

	for _, test := range testCases {
		test := test
		t.Run(test.desc, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.accepted, AcceptsBr(test.reqEncoding))
		})
	}
}

func generateBytes(length int) []byte {
	var value []byte
	for i := 0; i < length; i++ {
		value = append(value, 0x61+byte(i))
	}
	return value
}
