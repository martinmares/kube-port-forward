package diagnostic

import (
	"bufio"
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Proxy struct {
	evaluator      *Evaluator
	token          string
	connectTimeout time.Duration
	idleTimeout    time.Duration
	connections    chan struct{}
	logger         *slog.Logger
}

func NewProxy(cfg RuntimeConfig, token string, extraDeniedIP []net.IP, logger *slog.Logger) (*Proxy, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("proxy token is required")
	}
	evaluator, err := NewEvaluator(cfg.Policy, net.DefaultResolver, extraDeniedIP)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Proxy{
		evaluator:      evaluator,
		token:          token,
		connectTimeout: cfg.ConnectDuration(),
		idleTimeout:    cfg.IdleDuration(),
		connections:    make(chan struct{}, cfg.MaxConnections),
		logger:         logger,
	}, nil
}

func (p *Proxy) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(w, "ok\n")
			return
		}
		if r.Method == http.MethodConnect {
			p.handleConnect(w, r)
			return
		}
		http.NotFound(w, r)
	})
}

func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	if !p.authorized(r.Header.Get("Proxy-Authorization")) {
		w.Header().Set("Proxy-Authenticate", "Bearer")
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	select {
	case p.connections <- struct{}{}:
		defer func() { <-p.connections }()
	default:
		http.Error(w, "connection limit reached", http.StatusServiceUnavailable)
		return
	}

	host, portValue, err := net.SplitHostPort(r.Host)
	if err != nil {
		http.Error(w, "CONNECT target must be host:port", http.StatusBadRequest)
		return
	}
	port, err := strconv.Atoi(portValue)
	if err != nil || port < 1 || port > 65535 {
		http.Error(w, "invalid target port", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), p.connectTimeout)
	defer cancel()
	addresses, err := p.evaluator.ResolveAllowed(ctx, host, port)
	if err != nil {
		p.logger.Warn("connection denied", "target", r.Host, "err", err)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	upstream, err := dialAddresses(ctx, addresses, port)
	if err != nil {
		p.logger.Warn("connection failed", "target", r.Host, "err", err)
		http.Error(w, "target connection failed", http.StatusBadGateway)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "connection hijacking unavailable", http.StatusInternalServerError)
		return
	}
	client, buffer, err := hijacker.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	if _, err := buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		client.Close()
		upstream.Close()
		return
	}
	if err := buffer.Flush(); err != nil {
		client.Close()
		upstream.Close()
		return
	}

	p.logger.Info("connection opened", "target", r.Host)
	proxyStreams(client, upstream, p.idleTimeout)
	p.logger.Info("connection closed", "target", r.Host)
}

func (p *Proxy) authorized(header string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	provided := strings.TrimPrefix(header, prefix)
	if len(provided) != len(p.token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(p.token)) == 1
}

func dialAddresses(ctx context.Context, addresses []net.IPAddr, port int) (net.Conn, error) {
	dialer := net.Dialer{}
	var lastErr error
	for _, address := range addresses {
		connection, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(address.IP.String(), strconv.Itoa(port)))
		if err == nil {
			return connection, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func proxyStreams(client, upstream net.Conn, idleTimeout time.Duration) {
	defer client.Close()
	defer upstream.Close()

	client = &idleConn{Conn: client, timeout: idleTimeout}
	upstream = &idleConn{Conn: upstream, timeout: idleTimeout}
	var once sync.Once
	done := make(chan struct{})
	copyStream := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		once.Do(func() { close(done) })
	}
	go copyStream(upstream, client)
	go copyStream(client, upstream)
	<-done
}

type idleConn struct {
	net.Conn
	timeout time.Duration
}

func (c *idleConn) Read(buffer []byte) (int, error) {
	_ = c.Conn.SetReadDeadline(time.Now().Add(c.timeout))
	return c.Conn.Read(buffer)
}

func (c *idleConn) Write(buffer []byte) (int, error) {
	_ = c.Conn.SetWriteDeadline(time.Now().Add(c.timeout))
	return c.Conn.Write(buffer)
}

func ConnectThroughProxy(ctx context.Context, proxyAddress, token, target string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(target)
	if err != nil || strings.TrimSpace(host) == "" || strings.ContainsAny(host, " \t\r\n/\\") {
		return nil, fmt.Errorf("invalid proxy target")
	}
	parsedPort, err := strconv.Atoi(port)
	if err != nil || parsedPort < 1 || parsedPort > 65535 {
		return nil, fmt.Errorf("invalid proxy target")
	}
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "tcp", proxyAddress)
	if err != nil {
		return nil, err
	}
	request := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\nProxy-Authorization: Bearer " + token + "\r\n\r\n"
	if _, err := io.WriteString(connection, request); err != nil {
		connection.Close()
		return nil, err
	}
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		connection.Close()
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
		connection.Close()
		return nil, fmt.Errorf("proxy refused %s: %s: %s", target, response.Status, strings.TrimSpace(string(message)))
	}
	return &bufferedConn{Conn: connection, reader: reader}, nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(buffer []byte) (int, error) {
	return c.reader.Read(buffer)
}
