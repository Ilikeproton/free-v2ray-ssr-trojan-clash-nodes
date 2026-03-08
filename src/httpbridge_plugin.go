package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

type HTTPBridge struct {
	socksPort int
	httpPort  int
	socksAddr string

	mu       sync.Mutex
	running  bool
	server   *http.Server
	listener net.Listener

	transport *http.Transport
	logFn     func(format string, args ...any)
}

func NewHTTPBridge(socksPort int, httpPort int, logFn func(format string, args ...any)) (*HTTPBridge, error) {
	if socksPort <= 0 || socksPort > 65535 {
		return nil, errors.New("invalid socks port")
	}
	if httpPort <= 0 || httpPort > 65535 {
		return nil, errors.New("invalid http port")
	}
	if socksPort == httpPort {
		return nil, errors.New("http port cannot equal socks port")
	}

	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	dialer, err := proxy.SOCKS5("tcp", socksAddr, nil, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("create socks5 dialer failed: %w", err)
	}

	bridge := &HTTPBridge{
		socksPort: socksPort,
		httpPort:  httpPort,
		socksAddr: socksAddr,
		logFn:     logFn,
	}

	bridge.transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if isHTTPBridgeLocalAddress(addr) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			}
			return dialer.Dial(network, addr)
		},
		TLSHandshakeTimeout: 10 * time.Second,
		DisableKeepAlives:   false,
	}
	bridge.server = &http.Server{
		Addr:    fmt.Sprintf("0.0.0.0:%d", httpPort),
		Handler: http.HandlerFunc(bridge.handleHTTP),
	}
	return bridge, nil
}

func (b *HTTPBridge) Start() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running {
		return nil
	}

	ln, err := net.Listen("tcp", b.server.Addr)
	if err != nil {
		return err
	}
	b.listener = ln
	b.running = true

	go func() {
		err := b.server.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			b.logf("serve failed: %v", err)
		}
		b.mu.Lock()
		b.running = false
		b.listener = nil
		b.mu.Unlock()
	}()

	b.logf("started: listen=:%d via socks=%s", b.httpPort, b.socksAddr)
	return nil
}

func (b *HTTPBridge) Stop() error {
	b.mu.Lock()
	if !b.running {
		b.mu.Unlock()
		return nil
	}
	srv := b.server
	b.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := srv.Shutdown(ctx)
	if err != nil {
		_ = srv.Close()
	}
	if b.transport != nil {
		b.transport.CloseIdleConnections()
	}
	b.mu.Lock()
	b.running = false
	b.listener = nil
	b.mu.Unlock()
	b.logf("stopped: listen=:%d", b.httpPort)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

func (b *HTTPBridge) handleHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodConnect {
		b.handleConnect(w, req)
		return
	}
	b.handleForward(w, req)
}

func (b *HTTPBridge) handleConnect(w http.ResponseWriter, req *http.Request) {
	target := ensureHostPort(req.Host, "443")
	destConn, err := b.transport.DialContext(req.Context(), "tcp", target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		destConn.Close()
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		destConn.Close()
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	if _, err := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		clientConn.Close()
		destConn.Close()
		return
	}
	go bridgeTransfer(destConn, clientConn)
	go bridgeTransfer(clientConn, destConn)
}

func (b *HTTPBridge) handleForward(w http.ResponseWriter, req *http.Request) {
	removeHopByHopHeaders(req.Header)
	targetURL := req.URL.String()
	if req.URL.Scheme == "" {
		targetURL = "http://" + req.Host + req.URL.RequestURI()
	}
	newReq, err := http.NewRequest(req.Method, targetURL, req.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	newReq.Header = req.Header.Clone()
	newReq = newReq.WithContext(req.Context())

	resp, err := b.transport.RoundTrip(newReq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()

	removeHopByHopHeaders(resp.Header)
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (b *HTTPBridge) logf(format string, args ...any) {
	if b.logFn == nil {
		return
	}
	b.logFn(format, args...)
}

func bridgeTransfer(dst io.WriteCloser, src io.ReadCloser) {
	defer dst.Close()
	defer src.Close()
	_, _ = io.Copy(dst, src)
}

func removeHopByHopHeaders(header http.Header) {
	hopHeaders := []string{
		"Connection",
		"Proxy-Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	}
	for _, h := range hopHeaders {
		header.Del(h)
	}
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func ensureHostPort(hostPort string, defaultPort string) string {
	if strings.TrimSpace(hostPort) == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(hostPort); err == nil {
		return hostPort
	}
	return net.JoinHostPort(hostPort, defaultPort)
}

func isHTTPBridgeLocalAddress(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "127.0.0.1" || host == "localhost" || host == "::1" {
		return true
	}
	if strings.HasPrefix(host, "192.168.") || strings.HasPrefix(host, "10.") || strings.HasPrefix(host, "172.16.") {
		return true
	}
	return false
}
