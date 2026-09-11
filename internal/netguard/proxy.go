// Package netguard confines untrusted document-rendering network traffic to
// public HTTP(S) destinations. It does not replace OS process isolation.
package netguard

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var ErrForbidden = errors.New("URL destination is not permitted")
var blocked = func() []netip.Prefix {
	var result []netip.Prefix
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "64:ff9b::/96", "64:ff9b:1::/48", "2001::/32", "2001:db8::/32", "2002::/16"} {
		result = append(result, netip.MustParsePrefix(cidr))
	}
	return result
}()

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, block := range blocked {
		if block.Contains(ip) {
			return false
		}
	}
	return true
}

func ValidateURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Hostname() == "" || strings.Contains(u.Hostname(), "%") {
		return nil, ErrForbidden
	}
	if port := u.Port(); port != "" && port != "80" && port != "443" {
		return nil, ErrForbidden
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !publicIP(ip) {
		return nil, ErrForbidden
	}
	return u, nil
}

// CheckURL also rejects a private initial hostname before Chromium can render
// a proxy error page as a successful document. The proxy rechecks at dial time.
func CheckURL(ctx context.Context, raw string) error {
	u, err := ValidateURL(raw)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, u.Hostname())
	if err != nil {
		return err
	}
	if len(ips) == 0 {
		return ErrForbidden
	}
	for _, ip := range ips {
		addr, ok := netip.AddrFromSlice(ip.IP)
		if !ok || ip.Zone != "" || !publicIP(addr) {
			return ErrForbidden
		}
	}
	return nil
}

type resolver func(context.Context, string) ([]net.IPAddr, error)

func dialPublic(ctx context.Context, address string, lookup resolver, dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || (port != "80" && port != "443") {
		return nil, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, ErrForbidden
	}
	for _, ip := range ips {
		addr, ok := netip.AddrFromSlice(ip.IP)
		if !ok || ip.Zone != "" || !publicIP(addr) {
			return nil, ErrForbidden
		}
	}
	var last error
	for _, ip := range ips {
		// Dial the validated address itself: no second DNS resolution/rebinding gap.
		conn, err := dial(ctx, "tcp", net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	return nil, last
}

type Proxy struct {
	URL         string
	server      *http.Server
	transport   *http.Transport
	connections sync.Map
	closed      atomic.Bool
	slots       chan struct{}
	dial        func(context.Context, string) (net.Conn, error)
}

func Start(ctx context.Context) (*Proxy, error) {
	p := &Proxy{slots: make(chan struct{}, 16)}
	d := &net.Dialer{Timeout: 5 * time.Second}
	p.dial = func(ctx context.Context, address string) (net.Conn, error) {
		return dialPublic(ctx, address, net.DefaultResolver.LookupIPAddr, d.DialContext)
	}
	p.transport = &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, address string) (net.Conn, error) { return p.dial(ctx, address) }, ResponseHeaderTimeout: 15 * time.Second, MaxIdleConns: 16, MaxIdleConnsPerHost: 4, IdleConnTimeout: 15 * time.Second}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p.URL = "http://" + listener.Addr().String()
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 90 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 15 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() { _ = p.server.Serve(listener) }()
	return p, nil
}
func (p *Proxy) Close() {
	p.closed.Store(true)
	_ = p.server.Close()
	p.transport.CloseIdleConnections()
	p.connections.Range(func(key, value any) bool { _ = key.(net.Conn).Close(); return true })
}
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		http.Error(w, "render network capacity reached", 503)
		return
	}
	if r.Method == http.MethodConnect {
		p.tunnel(w, r)
		return
	}
	if _, err := ValidateURL(r.URL.String()); err != nil {
		http.Error(w, ErrForbidden.Error(), 403)
		return
	}
	outbound := r.Clone(r.Context())
	outbound.RequestURI = ""
	outbound.Header = r.Header.Clone()
	removeHopHeaders(outbound.Header)
	outbound.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	response, err := p.transport.RoundTrip(outbound)
	if err != nil {
		http.Error(w, "render resource unavailable", 502)
		return
	}
	defer response.Body.Close()
	removeHopHeaders(response.Header)
	for name, values := range response.Header {
		for _, v := range values {
			w.Header().Add(name, v)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(response.Body, 32<<20))
}
func (p *Proxy) tunnel(w http.ResponseWriter, r *http.Request) {
	upstream, err := p.dial(r.Context(), r.Host)
	if err != nil {
		http.Error(w, ErrForbidden.Error(), 403)
		return
	}
	defer upstream.Close()
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "tunnel unavailable", 500)
		return
	}
	client, buffer, err := hj.Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	p.connections.Store(client, true)
	p.connections.Store(upstream, true)
	defer p.connections.Delete(client)
	defer p.connections.Delete(upstream)
	if p.closed.Load() {
		return
	}
	deadline := time.Now().Add(90 * time.Second)
	_ = client.SetDeadline(deadline)
	_ = upstream.SetDeadline(deadline)
	if _, err = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err = buffer.Flush(); err != nil {
		return
	}
	done := make(chan struct{}, 1)
	go func() { _, _ = io.Copy(upstream, buffer); _ = upstream.Close(); done <- struct{}{} }()
	_, _ = io.Copy(client, upstream)
	_ = client.Close()
	<-done
}
func removeHopHeaders(header http.Header) {
	for _, name := range strings.Split(header.Get("Connection"), ",") {
		header.Del(strings.TrimSpace(name))
	}
	for _, name := range []string{"Connection", "Proxy-Connection", "Proxy-Authorization", "Proxy-Authenticate", "Keep-Alive", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
		header.Del(name)
	}
}
