package acquisition

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"pdfnest-backend/internal/analyzer/engine"
)

var blockedCIDRs []*net.IPNet

func init() {
	rawCIDRs := []string{
		// IPv4 Private / Loopback / Link-Local / Carrier-Grade NAT
		"127.0.0.0/8",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16",
		"0.0.0.0/8",
		"100.64.0.0/10",
		"198.18.0.0/15",
		// IPv6 Loopback / Unique Local / Link-Local
		"::1/128",
		"::/128",
		"fc00::/7",
		"fe80::/10",
	}

	for _, cidr := range rawCIDRs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err == nil {
			blockedCIDRs = append(blockedCIDRs, ipNet)
		}
	}
}

// DNSResolverFunc abstracts DNS IP lookup for testing and custom network environments.
type DNSResolverFunc func(host string) ([]net.IP, error)

// ValidateGitURL strictly inspects a remote Git repository URL for protocol security and SSRF threats.
func ValidateGitURL(rawURL string) error {
	return ValidateGitURLWithResolver(rawURL, net.LookupIP)
}

// ValidateGitURLWithResolver validates a Git URL using a custom DNS resolver.
func ValidateGitURLWithResolver(rawURL string, resolver DNSResolverFunc) error {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return fmt.Errorf("empty git url")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("malformed git url: %w", err)
	}

	// 1. Strict HTTPS Scheme
	if strings.ToLower(parsed.Scheme) != "https" {
		return ErrUnsupportedGitProtocol
	}

	// 2. Reject embedded user credentials
	if parsed.User != nil {
		return fmt.Errorf("embedded credentials in git url are prohibited for security")
	}

	hostname := parsed.Hostname()
	if hostname == "" {
		return fmt.Errorf("missing hostname in git url")
	}

	// 2.1 Enforce standard HTTPS port (port 443 or default empty port)
	port := parsed.Port()
	if port != "" && port != "443" {
		return fmt.Errorf("%w: custom port '%s' is prohibited; only standard HTTPS (port 443) is permitted", ErrSSRFBlocked, port)
	}

	lowerHost := strings.ToLower(hostname)

	// 3. Block obvious local/metadata hostnames
	blockedHosts := []string{
		"localhost",
		"127.0.0.1",
		"0.0.0.0",
		"::1",
		"169.254.169.254",
		"metadata.google.internal",
		"instance-data",
	}
	for _, bh := range blockedHosts {
		if lowerHost == bh {
			return ErrSSRFBlocked
		}
	}
	if strings.HasSuffix(lowerHost, ".local") ||
		strings.HasSuffix(lowerHost, ".internal") ||
		strings.HasSuffix(lowerHost, ".localhost") {
		return ErrSSRFBlocked
	}

	// 4. DNS Pre-Flight Resolution & IP validation
	if resolver == nil {
		resolver = net.LookupIP
	}

	ips, err := resolver(hostname)
	if err != nil {
		// If DNS resolution fails, check if hostname itself parses as an IP
		if ip := net.ParseIP(hostname); ip != nil {
			if isIPBlocked(ip) {
				return ErrSSRFBlocked
			}
			return nil
		}
		return fmt.Errorf("dns lookup failed for host %s: %w", hostname, err)
	}

	for _, ip := range ips {
		if isIPBlocked(ip) {
			return ErrSSRFBlocked
		}
	}

	return nil
}

func isIPBlocked(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil {
		if ip4.IsLoopback() || ip4.IsPrivate() || ip4.IsLinkLocalUnicast() || ip4.IsLinkLocalMulticast() || ip4.IsUnspecified() {
			return true
		}
		for _, block := range blockedCIDRs {
			if block.Contains(ip4) {
				return true
			}
		}
		return false
	}

	// IPv6
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, block := range blockedCIDRs {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}

// DeriveRepositoryName extracts the repository name from a Git URL path.
func DeriveRepositoryName(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "repository"
	}
	path := strings.Trim(parsed.Path, "/")
	path = strings.TrimSuffix(path, ".git")
	base := filepath.Base(path)
	if base == "" || base == "." || base == "/" {
		return "repository"
	}
	return base
}

// CloneGitRepository executes a secure shallow clone into the target sandbox workspace.
func CloneGitRepository(
	ctx context.Context,
	gitURL string,
	sandbox *Sandbox,
	limits AcquisitionLimits,
) (*AcquisitionResult, error) {
	if sandbox == nil || sandbox.IsClosed() {
		return nil, ErrSandboxClosed
	}

	if err := ValidateGitURL(gitURL); err != nil {
		return nil, err
	}

	timeout := limits.GitTimeout
	if timeout <= 0 {
		timeout = 45 * time.Second
	}

	cloneCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	startTime := time.Now()

	// Prepare isolated git clone command with redirects strictly disabled
	cmd := exec.CommandContext(
		cloneCtx,
		"git",
		"-c", "http.followRedirects=false",
		"clone",
		"--depth", "1",
		"--no-tags",
		"--single-branch",
		gitURL,
		sandbox.RootPath,
	)

	// Strip unsafe environment variables
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"HOME=" + sandbox.RootPath,
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		_ = sandbox.Cleanup()
		return nil, fmt.Errorf("%w: %s (output: %s)", ErrGitCloneFailed, err, string(output))
	}

	// Capture commit hash via direct rev-parse
	var commitHash string
	revCmd := exec.CommandContext(cloneCtx, "git", "-C", sandbox.RootPath, "rev-parse", "HEAD")
	revCmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1"}
	if revOut, revErr := revCmd.Output(); revErr == nil {
		commitHash = strings.TrimSpace(string(revOut))
	}

	// Calculate file count and bytes
	var fileCount int
	var totalBytes int64
	_ = filepath.WalkDir(sandbox.RootPath, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			fileCount++
			if info, infoErr := d.Info(); infoErr == nil {
				totalBytes += info.Size()
			}
		}
		return nil
	})

	repoName := DeriveRepositoryName(gitURL)
	duration := time.Since(startTime).Milliseconds()

	return &AcquisitionResult{
		SandboxPath:           sandbox.RootPath,
		SourceType:            engine.SourceTypeGit,
		RepositoryName:        repoName,
		CommitHash:            commitHash,
		TotalFiles:            fileCount,
		TotalBytes:            totalBytes,
		AcquisitionDurationMs: duration,
	}, nil
}
