package acquisition

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateGitURL(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		expectError bool
		expectedErr error
	}{
		{
			name:        "Valid Public HTTPS URL",
			url:         "https://github.com/torvalds/linux.git",
			expectError: false,
		},
		{
			name:        "Valid Public GitLab HTTPS URL",
			url:         "https://gitlab.com/gitlab-org/gitlab.git",
			expectError: false,
		},
		{
			name:        "Reject HTTP protocol",
			url:         "http://github.com/user/repo.git",
			expectError: true,
			expectedErr: ErrUnsupportedGitProtocol,
		},
		{
			name:        "Reject file:// protocol",
			url:         "file:///etc/passwd",
			expectError: true,
			expectedErr: ErrUnsupportedGitProtocol,
		},
		{
			name:        "Reject ssh:// protocol",
			url:         "ssh://git@github.com/user/repo.git",
			expectError: true,
			expectedErr: ErrUnsupportedGitProtocol,
		},
		{
			name:        "Reject Embedded Credentials",
			url:         "https://apikey:secret123@github.com/user/repo.git",
			expectError: true,
		},
		{
			name:        "Reject Localhost Hostname",
			url:         "https://localhost/user/repo.git",
			expectError: true,
			expectedErr: ErrSSRFBlocked,
		},
		{
			name:        "Reject IPv4 Loopback",
			url:         "https://127.0.0.1/user/repo.git",
			expectError: true,
			expectedErr: ErrSSRFBlocked,
		},
		{
			name:        "Reject IPv4 Private 10.0.0.0/8",
			url:         "https://10.1.2.3/user/repo.git",
			expectError: true,
			expectedErr: ErrSSRFBlocked,
		},
		{
			name:        "Reject IPv4 Private 192.168.0.0/16",
			url:         "https://192.168.1.100/user/repo.git",
			expectError: true,
			expectedErr: ErrSSRFBlocked,
		},
		{
			name:        "Reject Cloud Metadata 169.254.169.254",
			url:         "https://169.254.169.254/latest/meta-data",
			expectError: true,
			expectedErr: ErrSSRFBlocked,
		},
		{
			name:        "Reject IPv6 Loopback",
			url:         "https://[::1]/user/repo.git",
			expectError: true,
			expectedErr: ErrSSRFBlocked,
		},
		{
			name:        "Accept Explicit HTTPS Port 443",
			url:         "https://github.com:443/user/repo.git",
			expectError: false,
		},
		{
			name:        "Reject Custom Port 80",
			url:         "https://github.com:80/user/repo.git",
			expectError: true,
			expectedErr: ErrSSRFBlocked,
		},
		{
			name:        "Reject Custom Port 8080",
			url:         "https://github.com:8080/user/repo.git",
			expectError: true,
			expectedErr: ErrSSRFBlocked,
		},
		{
			name:        "Reject Custom Port 22",
			url:         "https://github.com:22/user/repo.git",
			expectError: true,
			expectedErr: ErrSSRFBlocked,
		},
		{
			name:        "Reject IPv6 RFC4193 Unique Local fc00::/7",
			url:         "https://[fc00::1]/user/repo.git",
			expectError: true,
			expectedErr: ErrSSRFBlocked,
		},
		{
			name:        "Reject IPv6 Link-Local fe80::/10",
			url:         "https://[fe80::1]/user/repo.git",
			expectError: true,
			expectedErr: ErrSSRFBlocked,
		},
		{
			name:        "Reject Empty URL",
			url:         "",
			expectError: true,
		},
	}

	mockResolver := func(host string) ([]net.IP, error) {
		switch host {
		case "github.com":
			return []net.IP{net.ParseIP("140.82.121.4")}, nil
		case "gitlab.com":
			return []net.IP{net.ParseIP("172.65.251.78")}, nil
		case "127.0.0.1":
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		case "10.1.2.3":
			return []net.IP{net.ParseIP("10.1.2.3")}, nil
		case "192.168.1.100":
			return []net.IP{net.ParseIP("192.168.1.100")}, nil
		case "169.254.169.254":
			return []net.IP{net.ParseIP("169.254.169.254")}, nil
		case "::1":
			return []net.IP{net.ParseIP("::1")}, nil
		case "fc00::1":
			return []net.IP{net.ParseIP("fc00::1")}, nil
		case "fe80::1":
			return []net.IP{net.ParseIP("fe80::1")}, nil
		default:
			return nil, net.UnknownNetworkError("unknown host")
		}
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateGitURLWithResolver(tt.url, mockResolver)
			if tt.expectError {
				assert.Error(t, err)
				if tt.expectedErr != nil {
					assert.ErrorIs(t, err, tt.expectedErr)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestDeriveRepositoryName(t *testing.T) {
	assert.Equal(t, "react", DeriveRepositoryName("https://github.com/facebook/react.git"))
	assert.Equal(t, "pdfnest-backend", DeriveRepositoryName("https://github.com/platen/pdfnest-backend"))
	assert.Equal(t, "repository", DeriveRepositoryName("https://github.com/"))
}
