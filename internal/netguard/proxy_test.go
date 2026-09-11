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
