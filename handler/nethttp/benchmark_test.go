package wasm_test

import (
	"bytes"
	"context"
	_ "embed"
	"net/http"
	"strings"
	"testing"

	nethttp "github.com/httpwasm/http-wasm-host-go/handler/nethttp"
	"github.com/httpwasm/http-wasm-host-go/internal/test"
)

var (
	smallBody []byte
	largeSize int
	largeBody []byte
)

func init() {
	noopHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	smallBody = []byte("hello world")
	largeSize = 4096 // 2x the read buffer size
	largeBody = make([]byte, largeSize)
	for i := 0; i < largeSize/2; i++ {
		largeBody[i] = 'a'
	}
	for i := largeSize / 2; i < largeSize; i++ {
		largeBody[i] = 'b'
	}
}

func get(url string) (req *http.Request) {
	req, _ = http.NewRequest(http.MethodGet, url+"/v1.0/hi", nil)
	return
}

func getWithLargeHeader(url string) (req *http.Request) {
	req, _ = http.NewRequest(http.MethodGet, url+"/v1.0/hi", nil)
	req.Header.Add("data", string(largeBody))
	return
}

func getWithQuery(url string) (req *http.Request) {
	req, _ = http.NewRequest(http.MethodGet, url+"/v1.0/hi?name=panda", nil)
	return
}

func getWithoutHeaders(url string) (req *http.Request) {
	req, _ = http.NewRequest(http.MethodGet, url+"/v1.0/hi", nil)
	req.Header = http.Header{}
	return
}

func post(url string) (req *http.Request) {
	body := bytes.NewReader(smallBody)
	req, _ = http.NewRequest(http.MethodPost, url, body)
	return
}

func postLarge(url string) (req *http.Request) {
	body := bytes.NewReader(largeBody)
	req, _ = http.NewRequest(http.MethodPost, url, body)
	return
}

func requestExampleWASI(url string) (req *http.Request) {
	body := strings.NewReader(`{"hello": "panda"}`)
	req, _ = http.NewRequest(http.MethodPost, url+"/v1.0/hi?name=panda", body)
	req.Header.Set("Content-Type", "application/json")
	req.Host = "localhost"
	return
}

var handlerExampleWASI = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Add("Set-Cookie", "a=b") // rewrite of multiple headers
	w.Header().Add("Set-Cookie", "c=d")

	// Use chunked encoding so we can set a test trailer
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("Trailer", "grpc-status")
	w.Header().Set(http.TrailerPrefix+"grpc-status", "1")
	w.Write([]byte(`{"hello": "world"}`)) // nolint
})

var benches = map[string]struct {
	bin     []byte
	next    http.Handler
	request func(url string) *http.Request
}{
	"example wasi": {
		bin:     test.BinExampleWASI,
		next:    handlerExampleWASI,
		request: requestExampleWASI,
	},
	"example router guest response": {
		bin: test.BinExampleRouter,
		request: func(url string) (req *http.Request) {
			req, _ = http.NewRequest(http.MethodGet, url, nil)
			return
		},
	},
	"example router host response": {
		bin: test.BinExampleRouter,
		request: func(url string) (req *http.Request) {
			req, _ = http.NewRequest(http.MethodGet, url+"/host", nil)
			return
		},
	},
	"log": {
		bin:     test.BinBenchLog,
		request: get,
	},
	"get_uri": {
		bin:     test.BinBenchGetURI,
		request: get,
	},
	"set_uri": {
		bin:     test.BinBenchSetURI,
		request: get,
	},
	"get_header_names none": {
		bin:     test.BinBenchGetHeaderNames,
		request: getWithoutHeaders,
	},
	"get_header_names": {
		bin:     test.BinBenchGetHeaderNames,
		request: get,
	},
	"get_header_names large": {
		bin:     test.BinBenchGetHeaderNames,
		request: getWithLargeHeader,
	},
	"get_header_values exists": {
		bin:     test.BinBenchGetHeaderValues,
		request: get,
	},
	"get_header_values not exists": {
		bin:     test.BinBenchGetHeaderValues,
		request: getWithoutHeaders,
	},
	"set_header_value": {
		bin:     test.BinBenchSetHeaderValue,
		request: get,
	},
	"add_header_value": {
		bin:     test.BinBenchAddHeaderValue,
		request: get,
	},
	"remove_header": {
		bin:     test.BinBenchRemoveHeader,
		request: get,
	},
	"read_body": {
		bin:     test.BinBenchReadBody,
		request: post,
	},
	"read_body_stream": {
		bin:     test.BinBenchReadBodyStream,
		request: post,
	},
	"read_body_stream large": {
		bin:     test.BinBenchReadBodyStream,
		request: postLarge,
	},
	"set_status_code": {
		bin:     test.BinBenchSetStatusCode,
		request: get,
	},
	"write_body": {
		bin:     test.BinBenchWriteBody,
		request: get,
	},
}

func Benchmark(b *testing.B) {
	for n, s := range benches {
		s := s
		b.Run(n, func(b *testing.B) {
			benchmark(b, n, s.bin, s.next, s.request)
		})
	}
}

func benchmark(b *testing.B, name string, bin []byte, handler http.Handler, newRequest func(string) *http.Request) {
	ctx := context.Background()

	mw, err := nethttp.NewMiddleware(ctx, bin)
	if err != nil {
		b.Fatal(err)
	}
	defer mw.Close(ctx)

	if handler == nil {
		handler = noopHandler
	}
	h := mw.NewHandler(ctx, handler)

	b.Run(name, func(b *testing.B) {
		// We don't report allocations because memory allocations for TinyGo are
		// in wasm which isn't visible to the Go benchmark.
		for i := 0; i < b.N; i++ {
			h.ServeHTTP(fakeResponseWriter{}, newRequest("http://localhost"))
		}
	})
}

var _ http.ResponseWriter = fakeResponseWriter{}

type fakeResponseWriter struct{}

func (rw fakeResponseWriter) Header() http.Header {
	return http.Header{}
}

func (rw fakeResponseWriter) Write(b []byte) (int, error) {
	return len(b), nil
}

func (rw fakeResponseWriter) WriteHeader(statusCode int) {
	// None of our benchmark tests should send failure status. If there's a
	// failure, it is likely there's a problem in the test data.
	if statusCode == 500 {
		panic(statusCode)
	}
}
