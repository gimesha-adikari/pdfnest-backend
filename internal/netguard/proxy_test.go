package netguard

import (
	"context"
	"github.com/stretchr/testify/require"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicDestinationsOnly(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.100.100.200", "192.168.1.1", "0.0.0.0", "224.0.0.1", "::1", "::ffff:127.0.0.1", "fc00::1", "64:ff9b::a00:1"} {
		require.False(t, publicIP(netip.MustParseAddr(ip)), ip)
	}
	require.True(t, publicIP(netip.MustParseAddr("93.184.216.34")))
	for _, raw := range []string{"file:///etc/passwd", "ftp://example.com", "http://127.0.0.1", "http://[::1]", "http://example.com:22", "http://user:pass@example.com"} {
		_, err := ValidateURL(raw)
		require.Error(t, err, raw)
	}
}

func TestProxyRechecksRedirectDestination(t *testing.T) {
	var privateHits atomic.Int32
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		privateHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer private.Close()
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, private.URL, http.StatusFound)
	}))
	defer public.Close()
	proxy, err := Start(context.Background())
	require.NoError(t, err)
	defer proxy.Close()
	// Replace only this controlled public origin's socket; every redirect still
	// traverses the real proxy URL and private-destination checks.
	normalDial := proxy.dial
	proxy.dial = func(ctx context.Context, address string) (net.Conn, error) {
		if address == "public.example:80" {
			return (&net.Dialer{}).DialContext(ctx, "tcp", public.Listener.Addr().String())
		}
		return normalDial(ctx, address)
	}
	u, _ := url.Parse(proxy.URL)
	transport := &http.Transport{Proxy: http.ProxyURL(u)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	resp, err := client.Get("http://public.example/")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Zero(t, privateHits.Load())
}
func TestDialPinsValidatedAddressAndRejectsMixedDNS(t *testing.T) {
	calls := 0
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		calls++
		require.Equal(t, "93.184.216.34:443", address)
		a, b := net.Pipe()
		b.Close()
		return a, nil
	}
	lookup := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}
	conn, err := dialPublic(context.Background(), "public.example:443", lookup, dial)
	require.NoError(t, err)
	conn.Close()
	require.Equal(t, 1, calls)
	mixed := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}, {IP: net.ParseIP("127.0.0.1")}}, nil
	}
	_, err = dialPublic(context.Background(), "rebound.example:443", mixed, dial)
	require.ErrorIs(t, err, ErrForbidden)
	require.Equal(t, 1, calls)
}
func TestProxyBlocksLocalRequestsAndConnect(t *testing.T) {
	proxy, err := Start(context.Background())
	require.NoError(t, err)
	defer proxy.Close()
	u, _ := url.Parse(proxy.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}}
	for _, target := range []string{"http://127.0.0.1/", "http://169.254.169.254/", "http://localhost/"} {
		resp, err := client.Get(target)
		require.NoError(t, err)
		require.True(t, resp.StatusCode == 403 || resp.StatusCode == 502)
		resp.Body.Close()
	}
	request := httptest.NewRequest("CONNECT", "http://127.0.0.1:443", nil)
	request.Host = "127.0.0.1:443"
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, request)
	require.Equal(t, 403, rec.Code)
}

func TestGitProxyRestrictsAuthorityAndRevalidatesRebinding(t *testing.T) {
	p, err := StartGit(context.Background(), "repo.example", 5*time.Second)
	require.NoError(t, err)
	defer p.Close()
	calls := 0
	lookup := func(context.Context, string) ([]net.IPAddr, error) {
		calls++
		if calls == 1 {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}
	sockets := 0
	dial := func(_ context.Context, _, address string) (net.Conn, error) {
		sockets++
		require.Equal(t, "93.184.216.34:443", address)
		a, b := net.Pipe()
		b.Close()
		return a, nil
	}
	p.dial = func(ctx context.Context, address string) (net.Conn, error) {
		return dialPublic(ctx, address, lookup, dial)
	}
	c, err := p.dial(context.Background(), "repo.example:443")
	require.NoError(t, err)
	c.Close()
	// Same hostname, changed DNS: the connection-time lookup must reject it.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("CONNECT", "http://repo.example:443", nil)
	req.Host = "repo.example:443"
	p.ServeHTTP(rec, req)
	require.Equal(t, 403, rec.Code)
	require.Equal(t, 1, sockets)
	for _, target := range []string{"other.example:443", "repo.example:80", "[::1]:443"} {
		req := httptest.NewRequest("CONNECT", "http://"+target, nil)
		req.Host = target
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)
		require.Equal(t, 403, rec.Code)
	}
	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "http://repo.example/", nil))
	require.Equal(t, 403, rec.Code)
	require.Equal(t, 2, calls, "forbidden authorities never resolve or connect")
}

func TestDialIPv6PinsAndRejectsForbiddenForms(t *testing.T) {
	for _, ip := range []string{"2606:4700:4700::1111", "::ffff:127.0.0.1", "fe80::1", "64:ff9b::7f00:1", "ff02::1", "2001:db8::1"} {
		calls := 0
		lookup := func(context.Context, string) ([]net.IPAddr, error) { return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil }
		c, err := dialPublic(context.Background(), "repo.example:443", lookup, func(_ context.Context, _, addr string) (net.Conn, error) {
			calls++
			require.Equal(t, "["+ip+"]:443", addr)
			a, b := net.Pipe()
			b.Close()
			return a, nil
		})
		if ip == "2606:4700:4700::1111" {
			require.NoError(t, err)
			c.Close()
			require.Equal(t, 1, calls)
		} else {
			require.ErrorIs(t, err, ErrForbidden)
			require.Zero(t, calls)
		}
	}
}
