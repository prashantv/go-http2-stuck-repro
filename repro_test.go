package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"io/ioutil"
	"math"
	"net"
	"net/http"
	_ "net/http/pprof"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

var flagSingleStuck = flag.Int("num-stuck-calls", 5, "Number of calls to make to /stuck after the large payload call.")

func init() {
	// Start a pprof server for debugging stack traces
	go http.ListenAndServe("0.0.0.0:9999", nil)
	fmt.Println("Listening on :9999")
}

func TestSendLargeUnreadPayload(t *testing.T) {
	stuckLn := startBlockingTCP(t)
	stuckURL := fmt.Sprintf("http://%v", stuckLn.Addr())
	normal1URL := startHTTP2Server(t, echoHandler)

	client := newHTTP2Client()

	// Start a goroutine that keeps writing to the stuck connection
	// This will fill up the write buffer for the TCP connection to stuckURL
	clientReader := &infiniteReader{}
	go client.Post(stuckURL, "application/raw", clientReader)
	time.Sleep(time.Second)

	for i := 0; i < *flagSingleStuck; i++ {
		go client.Get(stuckURL)
	}

	// Do another call to stuckURL which will grab the clientConnPool lock
	for i := 0; i < 10; i++ {
		fmt.Println("Making request", i)

		_, err := client.Post(normal1URL, "application/raw", strings.NewReader("small payload"))
		require.NoError(t, err, "POST failed")

		// Ensure that we can still do a small echo request/response.
		echoTest(t, client, normal1URL)
	}
}

func echoTest(t *testing.T, client *http.Client, url string) {
	const data = `{"hello": "world"}`
	res, err := client.Post(url, "application/json", strings.NewReader(data))
	require.NoError(t, err, "echo: POST failed")

	got, err := ioutil.ReadAll(res.Body)
	require.NoError(t, err, "echo: failed to read response body")
	assert.Equal(t, http.StatusOK, res.StatusCode, "echo: unexpected response code")
	assert.Equal(t, data, string(got), "echo: unexpected response")
}

func startHTTP2Server(t *testing.T, delegate http.HandlerFunc) string {
	ln, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err, "failed to listen")

	server := newHTTP2Server(delegate)
	go server.Serve(ln)
	return "http://" + ln.Addr().String()
}

func newHTTP2Server(delegate http.Handler) *http.Server {
	return &http.Server{
		Handler: h2c.NewHandler(delegate, &http2.Server{
			// Need to make sure we hit the TCP write buffer before we hit conn flow control
			MaxUploadBufferPerConnection: math.MaxInt32,
			MaxUploadBufferPerStream:     math.MaxInt32,
		}),
	}
}

func startBlockingTCP(t *testing.T) net.Listener {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "listen failed")

	go func() {
		for {
			conn, _ := ln.Accept()
			if conn, ok := conn.(*net.TCPConn); ok {
				// Set a small read buffer so we're more likely to hit the write buffer limit on the client.
				conn.SetReadBuffer(10)
			}
		}
	}()

	return ln
}

func newHTTP2Client() *http.Client {
	return &http.Client{
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLS: func(network, addr string, _ *tls.Config) (net.Conn, error) {
				conn, err := net.Dial(network, addr)
				if conn, ok := conn.(*net.TCPConn); ok {
					if err := conn.SetWriteBuffer(100); err != nil {
						panic(err)
					}
				}
				return conn, err
			},
		},
		Timeout: time.Minute,
	}
}

type infiniteReader struct {
	read int64
}

func (r *infiniteReader) Read(p []byte) (int, error) {
	atomic.AddInt64(&r.read, int64(len(p)))
	return len(p), nil
}

func echoHandler(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err == nil {
		_, err = w.Write(body)
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
