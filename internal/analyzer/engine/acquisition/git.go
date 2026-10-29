package acquisition

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/netguard"
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
	// Session creation and queued-job validation also resolve DNS through this
	// boundary, so required storage policy must refuse those paths as well.
	if err := checkGitStoragePolicy(); err != nil {
		return err
	}
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
	if parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" {
		return fmt.Errorf("embedded credentials in git url are prohibited for security")
	}

	if _, err := netguard.ValidateURL(trimmed); err != nil {
		return ErrSSRFBlocked
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
		return fmt.Errorf("%w: dns lookup failed for host %s: %v", ErrGitUnreachable, hostname, err)
	}

	if len(ips) == 0 {
		return ErrSSRFBlocked
	}
	for _, ip := range ips {
		if isIPBlocked(ip) {
			return ErrSSRFBlocked
		}
	}

	return nil
}

func isIPBlocked(ip net.IP) bool {
	if !netguard.IsPublicIP(ip) {
		return true
	}
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
	return cloneGitRepository(ctx, gitURL, sandbox, limits, "/usr/bin/git")
}

// gitBinary is an internal test seam; public acquisition always uses the system
// Git and a fixed helper PATH, never an inherited caller-supplied executable.
func cloneGitRepository(ctx context.Context, gitURL string, sandbox *Sandbox, limits AcquisitionLimits, gitBinary string) (*AcquisitionResult, error) {
	if sandbox == nil || sandbox.IsClosed() {
		return nil, ErrSandboxClosed
	}

	success := false
	defer func() {
		if !success {
			_ = sandbox.Cleanup()
		}
	}()
	// Check before DNS resolution, guarded transport, or any native process.
	if err := checkGitStoragePolicy(); err != nil {
		return nil, err
	}
	if err := sandbox.admitGitQuota(); err != nil {
		return nil, err
	}
	gitURL = strings.TrimSpace(gitURL)
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

	scratchRoot, home, temp, err := sandbox.gitScratch()
	if err != nil {
		return nil, fmt.Errorf("%w: prepare auxiliary scratch: %w", ErrGitAcquisitionFailed, err)
	}
	parsed, _ := url.Parse(gitURL) // Validated above.
	proxy, err := netguard.StartGit(cloneCtx, parsed.Hostname(), timeout)
	if err != nil {
		return nil, fmt.Errorf("%w: guarded transport unavailable", ErrGitAcquisitionFailed)
	}
	defer proxy.Close()
	args := []string{
		"-c", "http.followRedirects=false",
		"-c", "http.proxy=" + proxy.URL,
		"-c", "http.sslVerify=true",
		"-c", "protocol.allow=never",
		"-c", "protocol.https.allow=always",
		"-c", "credential.helper=",
		"-c", "core.hooksPath=/dev/null",
		"-c", "init.templateDir=",
		"clone", "--depth", "1", "--no-tags", "--single-branch", "--", gitURL, sandbox.RootPath,
	}
	cmd := exec.Command(gitBinary, args...)
	cmd.Dir = home
	cmd.Env = isolatedGitEnv(home, temp)
	if err := sandbox.confineGit(cmd); err != nil {
		return nil, err
	}
	output, err := runBoundedGit(cloneCtx, cmd, scratchRoot, limits)
	if errors.Is(err, ErrGitResourceLimit) {
		return nil, err
	}
	if err != nil {
		_ = sandbox.Cleanup()
		elapsed := time.Since(startTime).Round(time.Millisecond)
		outStr := strings.TrimSpace(string(output))

		// 1. Context Deadline / Timeout Diagnosis
		if errors.Is(cloneCtx.Err(), context.DeadlineExceeded) || (ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)) {
			return nil, fmt.Errorf("%w: repository clone timed out after %s (exceeded limit %s)", ErrGitTimeout, elapsed, timeout)
		}

		// 2. Parent Context Cancellation (e.g. user aborted or worker shutting down)
		if errors.Is(cloneCtx.Err(), context.Canceled) || (ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled)) {
			return nil, fmt.Errorf("%w: git clone operation canceled", ErrGitAcquisitionFailed)
		}

		// 3. Process Exit Code / Signal Inspection
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Exit code 137 = SIGKILL (Linux OS OOM killer or container resource cgroup limit)
			if exitErr.ExitCode() == 137 || exitErr.String() == "signal: killed" {
				return nil, fmt.Errorf("%w: git clone process terminated by system (exceeded memory or container resource limit)", ErrGitResourceLimit)
			}
		}

		// 4. Stderr pattern classification
		lowerOut := strings.ToLower(outStr)
		if strings.Contains(lowerOut, "git_storage_unavailable:") {
			return nil, fmt.Errorf("%w: %w", ErrGitAcquisitionFailed, ErrGitStorageUnavailable)
		}
		if strings.Contains(lowerOut, "disk quota exceeded") {
			return nil, fmt.Errorf("%w: kernel quota refused allocation", ErrGitResourceLimit)
		}
		if strings.Contains(lowerOut, "authentication failed") ||
			strings.Contains(lowerOut, "could not read username") ||
			strings.Contains(lowerOut, "terminal prompts disabled") {
			return nil, fmt.Errorf("%w: authentication required for repository", ErrGitAuthRequired)
		}

		if strings.Contains(lowerOut, "could not resolve host") ||
			strings.Contains(lowerOut, "failed to connect") ||
			strings.Contains(lowerOut, "connection refused") ||
			strings.Contains(lowerOut, "network is unreachable") {
			return nil, fmt.Errorf("%w: could not connect to git host (%s)", ErrGitUnreachable, outStr)
		}

		if strings.Contains(lowerOut, "repository not found") ||
			strings.Contains(lowerOut, "remote: not found") ||
			strings.Contains(lowerOut, "not found") {
			return nil, fmt.Errorf("%w: repository not found or inaccessible (%s)", ErrGitInvalidRepository, outStr)
		}

		return nil, fmt.Errorf("%w: %s (output: %s)", ErrGitAcquisitionFailed, err, outStr)
	}

	// Capture commit hash via direct rev-parse
	var commitHash string
	revCmd := exec.Command(gitBinary, "-C", sandbox.RootPath, "rev-parse", "HEAD")
	revCmd.Env = isolatedGitEnv(home, temp)
	revCmd.Dir = home
	if err := sandbox.confineGit(revCmd); err != nil {
		return nil, err
	}
	if revOut, revErr := runBoundedGit(cloneCtx, revCmd, scratchRoot, limits); revErr == nil {
		commitHash = strings.TrimSpace(string(revOut))
	} else if errors.Is(revErr, ErrGitResourceLimit) {
		return nil, revErr
	}
	// Keep scratch acceptance separate from repository provenance statistics.
	if _, _, err := checkGitWorkspace(cloneCtx, scratchRoot, effectiveGitLimits(limits)); err != nil {
		return nil, err
	}

	fileCount, totalBytes, err := checkGitWorkspace(cloneCtx, sandbox.RootPath, effectiveGitLimits(limits))
	if err != nil {
		return nil, err
	}
	if cloneCtx.Err() != nil {
		return nil, fmt.Errorf("%w: acquisition interrupted", ErrGitAcquisitionFailed)
	}
	success = true

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
