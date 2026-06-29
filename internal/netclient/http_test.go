package netclient

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHTTPClientDefault(t *testing.T) {
	client, err := HTTPClient(context.Background(), "", "", HTTPClientOptions{})
	require.NoError(t, err)
	require.IsType(t, &loggingRoundTripper{}, client.Transport)
	require.Equal(t, 5*time.Minute, client.Timeout)
}

func TestHTTPClientTimeoutOverride(t *testing.T) {
	client, err := HTTPClient(context.Background(), "", "", HTTPClientOptions{Timeout: time.Minute})
	require.NoError(t, err)
	require.Equal(t, time.Minute, client.Timeout)
}

func TestHTTPClientProxy(t *testing.T) {
	client, err := HTTPClient(context.Background(), "proxy-test", "http://127.0.0.1:8080", HTTPClientOptions{})
	require.NoError(t, err)
	require.NotSame(t, http.DefaultClient, client)
	require.NotNil(t, client.Transport)
}

func TestHTTPClientInvalidProxy(t *testing.T) {
	_, err := HTTPClient(context.Background(), "invalid-proxy", "http://[::1", HTTPClientOptions{})
	require.Error(t, err)
}

func TestHTTPClientSocks5HSendsDomainToProxy(t *testing.T) {
	const targetHost = "dns-must-stay-remote.invalid"

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { require.NoError(t, ln.Close()) }()

	result := make(chan socksResult, 1)
	go serveOneSocks5(ln, result)

	client, err := HTTPClient(context.Background(), "socks-test", "socks5h://"+ln.Addr().String(), HTTPClientOptions{})
	require.NoError(t, err)
	client.Timeout = 5 * time.Second

	_, err = client.Get("http://" + targetHost)
	require.Error(t, err)

	select {
	case res := <-result:
		require.NoError(t, res.err)
		require.Equal(t, targetHost, res.host)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for socks proxy request")
	}
}

type socksResult struct {
	host string
	err  error
}

func serveOneSocks5(ln net.Listener, result chan<- socksResult) {
	conn, err := ln.Accept()
	if err != nil {
		result <- socksResult{err: err}
		return
	}
	defer func() { _ = conn.Close() }()

	r := bufio.NewReader(conn)
	version, err := r.ReadByte()
	if err != nil {
		result <- socksResult{err: err}
		return
	}
	if version != 0x05 {
		result <- socksResult{err: fmt.Errorf("unexpected socks version %d", version)}
		return
	}
	methods, err := r.ReadByte()
	if err != nil {
		result <- socksResult{err: err}
		return
	}
	for range int(methods) {
		_, err := r.ReadByte()
		if err != nil {
			result <- socksResult{err: err}
			return
		}
	}
	_, err = conn.Write([]byte{0x05, 0x00})
	if err != nil {
		result <- socksResult{err: err}
		return
	}

	header := make([]byte, 4)
	_, err = io.ReadFull(r, header)
	if err != nil {
		result <- socksResult{err: err}
		return
	}
	if string(header) != string([]byte{0x05, 0x01, 0x00, 0x03}) {
		result <- socksResult{err: fmt.Errorf("unexpected socks request header %v", header)}
		return
	}

	nameLen, err := r.ReadByte()
	if err != nil {
		result <- socksResult{err: err}
		return
	}
	name := make([]byte, int(nameLen))
	_, err = io.ReadFull(r, name)
	if err != nil {
		result <- socksResult{err: err}
		return
	}
	_, err = io.ReadFull(r, make([]byte, 2))
	if err != nil {
		result <- socksResult{err: err}
		return
	}
	result <- socksResult{host: string(name)}

	_, err = conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	if err != nil {
		result <- socksResult{err: err}
	}
}
