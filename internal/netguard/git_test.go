package netguard

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Exercise real Git/libcurl through a CONNECT tunnel to a synthetic TLS origin.
// Only the final dial socket is mapped to loopback; no public request is sent.
func TestGitTunnelTLSHostnameAndRedirectPolicy(t *testing.T) {
	var hits atomic.Int32
	redirect := atomic.Bool{}
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if redirect.Load() {
			http.Redirect(w, r, "https://127.0.0.1/private.git", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		_, _ = w.Write([]byte("001e# service=git-upload-pack\n00000000"))
	}))
	defer origin.Close()
	// httptest's synthetic certificate covers example.com, not wrong.example.
	cert, err := x509.ParseCertificate(origin.TLS.Certificates[0].Certificate[0])
	require.NoError(t, err)
	require.NoError(t, cert.VerifyHostname("example.com"))
	ca := filepath.Join(t.TempDir(), "fixture-ca.pem")
	require.NoError(t, os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0600))
	for _, host := range []string{"example.com", "wrong.example"} {
		dial := func(ctx context.Context, address string) (net.Conn, error) {
			return dialPublic(ctx, address, func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
			}, func(ctx context.Context, network, literal string) (net.Conn, error) {
				require.Equal(t, "93.184.216.34:443", literal)
				return (&net.Dialer{}).DialContext(ctx, network, origin.Listener.Addr().String())
			})
		}
		p, err := start(context.Background(), net.JoinHostPort(host, "443"), 5*time.Second, dial)
		require.NoError(t, err)
		for _, mode := range []string{"normal", "redirect"} {
			redirect.Store(mode == "redirect")
			before := hits.Load()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			cmd := exec.CommandContext(ctx, "/usr/bin/git", "-c", "http.proxy="+p.URL, "-c", "http.sslCAInfo="+ca, "-c", "http.sslVerify=true", "-c", "http.followRedirects=false", "ls-remote", "https://"+host+"/repo.git")
			cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}
			output, err := cmd.CombinedOutput()
			cancel()
			if host == "example.com" && mode == "normal" {
				require.NoError(t, err, string(output))
				require.EqualValues(t, before+1, hits.Load())
			} else {
				require.Error(t, err, string(output))
			}
			if host == "wrong.example" {
				require.Equal(t, before, hits.Load(), "bad TLS hostname cannot send an HTTP request")
			}
			if mode == "redirect" && host == "example.com" {
				require.EqualValues(t, before+1, hits.Load(), "redirect is not followed")
			}
		}
		p.Close()
	}
}
