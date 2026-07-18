// Package netguard validates that a (host, port) pair is safe to connect to
// before any network I/O occurs. It is shared by the data-source connection-
// test handler (API) and the Worker's dynamic executor so both enforce the
// same rules.
//
// Design goals:
//   - Never accept private, loopback, link-local, multicast, unspecified,
//     or cloud-metadata addresses unless they appear in the allowlist.
//   - Reject all ports except those in AllowedPorts (default: 3306).
//   - Resolve all DNS records and validate every IP — not just the first.
//   - DNS-rebinding protection: pin the validated IP set in Dial so the
//     actual connection goes to an already-checked address rather than
//     re-resolving (which could yield a different IP after TTL expiry).
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// cloud metadata IP ranges that must never be reachable.
var cloudMetaCIDRs = mustParseCIDRs([]string{
	"169.254.169.254/32", // AWS / GCP / Azure IMDS (IPv4 link-local)
	"fd00:ec2::254/128",  // AWS IMDS (IPv6)
	"100.100.100.200/32", // Alibaba Cloud ECS metadata
})

// blockedCIDRs covers loopback, private, link-local, multicast, and
// unspecified address ranges for both IPv4 and IPv6.
var blockedCIDRs = mustParseCIDRs([]string{
	// Loopback
	"127.0.0.0/8",
	"::1/128",
	// RFC 1918 private
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	// Link-local
	"169.254.0.0/16",
	"fe80::/10",
	// Multicast
	"224.0.0.0/4",
	"ff00::/8",
	// Unspecified
	"0.0.0.0/8",
	"::/128",
	// Unique local (fc00::/7 covers fd00::/8 and fc00::/8)
	"fc00::/7",
})

// defaultAllowedPorts is the set of TCP ports accepted when the caller
// does not provide an explicit AllowedPorts list.
var defaultAllowedPorts = map[uint16]struct{}{3306: {}}

// Validator holds the immutable policy for a server lifetime.
type Validator struct {
	allowedPorts map[uint16]struct{}
	allowlist    []*net.IPNet // private ranges explicitly permitted (dev use)
	resolver     Resolver
}

// Resolver abstracts DNS lookups for testing.
type Resolver interface {
	LookupHost(ctx context.Context, host string) (addrs []string, err error)
}

// defaultResolver wraps the standard net package resolver.
type defaultResolver struct{}

func (defaultResolver) LookupHost(ctx context.Context, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}

// Config holds the options for NewValidator.
type Config struct {
	// AllowedPorts is the whitelist of TCP ports. Nil means {3306}.
	AllowedPorts []uint16
	// PrivateAllowlist is a list of CIDR strings whose addresses are
	// permitted even though they would otherwise be blocked.
	// Example: ["172.17.0.0/16"] for Docker bridge networks in dev.
	PrivateAllowlist []string
	// Resolver overrides DNS resolution; leave nil for the system resolver.
	Resolver Resolver
}

// NewValidator constructs a Validator from cfg.
// Returns an error if any PrivateAllowlist CIDR is malformed.
func NewValidator(cfg Config) (*Validator, error) {
	ports := defaultAllowedPorts
	if len(cfg.AllowedPorts) > 0 {
		ports = make(map[uint16]struct{}, len(cfg.AllowedPorts))
		for _, p := range cfg.AllowedPorts {
			ports[p] = struct{}{}
		}
	}

	allowlist := make([]*net.IPNet, 0, len(cfg.PrivateAllowlist))
	for _, cidr := range cfg.PrivateAllowlist {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("netguard: invalid allowlist CIDR %q: %w", cidr, err)
		}
		allowlist = append(allowlist, network)
	}

	r := cfg.Resolver
	if r == nil {
		r = defaultResolver{}
	}

	return &Validator{
		allowedPorts: ports,
		allowlist:    allowlist,
		resolver:     r,
	}, nil
}

// ValidatedTarget holds the result of a successful Validate call.
// The ResolvedIPs field is the canonical set that Dial must connect to —
// no re-resolution is permitted after this point.
type ValidatedTarget struct {
	Host        string   // original hostname (used as TLS ServerName)
	Port        uint16   // validated port
	ResolvedIPs []net.IP // all IPs resolved and validated; Dial pins to these
}

// Validate performs the full two-phase check:
//  1. String-level: port whitelist, no scheme/userinfo/path in host.
//  2. DNS-level: resolve ALL IPs, reject any that fall in blocked ranges
//     (unless the allowlist covers them).
//
// On success the caller must use Dial (or DialContext) from this package to
// open the actual connection so that the validated IPs are reused.
func (v *Validator) Validate(ctx context.Context, host string, port uint16) (*ValidatedTarget, error) {
	// Phase 1 — string-level checks.
	if err := v.validateHostString(host); err != nil {
		return nil, err
	}
	if _, ok := v.allowedPorts[port]; !ok {
		return nil, ErrPortNotAllowed
	}

	// Phase 2 — DNS resolution + IP checks.
	addrs, err := v.resolver.LookupHost(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("netguard: dns lookup: %w", err)
	}
	if len(addrs) == 0 {
		return nil, ErrNoAddressResolved
	}

	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip == nil {
			return nil, fmt.Errorf("netguard: resolver returned non-IP %q", addr)
		}
		if err := v.checkIP(ip); err != nil {
			return nil, err
		}
		ips = append(ips, ip)
	}

	return &ValidatedTarget{Host: host, Port: port, ResolvedIPs: ips}, nil
}

// DialContext opens a TCP connection to the target using ONLY the already-
// validated IPs from t.ResolvedIPs. It tries them in order and returns the
// first successful connection. This prevents DNS-rebinding: no further DNS
// resolution happens after Validate.
//
// The returned net.Conn must be closed by the caller.
func (v *Validator) DialContext(ctx context.Context, t *ValidatedTarget) (net.Conn, error) {
	addr := net.JoinHostPort(t.ResolvedIPs[0].String(), strconv.Itoa(int(t.Port)))
	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		// Try remaining IPs before giving up.
		for _, ip := range t.ResolvedIPs[1:] {
			a := net.JoinHostPort(ip.String(), strconv.Itoa(int(t.Port)))
			conn, err = dialer.DialContext(ctx, "tcp", a)
			if err == nil {
				break
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("netguard: dial: %w", err)
	}
	return conn, nil
}

// validateHostString rejects hosts that embed URI components (scheme, path,
// userinfo) which could be used to smuggle a different address into the DSN.
func (v *Validator) validateHostString(host string) error {
	if host == "" {
		return ErrInvalidHost
	}
	// Disallow characters that could smuggle URI components.
	for _, ch := range []string{"@", "/", "?", "#"} {
		if strings.Contains(host, ch) {
			return ErrInvalidHost
		}
	}
	// A bare IPv6 address must be bracketed when joined with a port; reject
	// unbracketed colons that are not an IPv6 literal.
	if !strings.HasPrefix(host, "[") && strings.Count(host, ":") > 1 {
		return ErrInvalidHost
	}
	return nil
}

// checkIP returns an error if ip falls in any blocked range and is not
// covered by the explicit allowlist.
func (v *Validator) checkIP(ip net.IP) error {
	// Allowlist takes precedence.
	for _, allow := range v.allowlist {
		if allow.Contains(ip) {
			return nil
		}
	}
	// Cloud metadata addresses.
	for _, cidr := range cloudMetaCIDRs {
		if cidr.Contains(ip) {
			return ErrAddressBlocked
		}
	}
	// General blocked ranges.
	for _, cidr := range blockedCIDRs {
		if cidr.Contains(ip) {
			return ErrAddressBlocked
		}
	}
	return nil
}

func mustParseCIDRs(cidrs []string) []*net.IPNet {
	result := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(fmt.Sprintf("netguard: invalid built-in CIDR %q: %v", cidr, err))
		}
		result = append(result, network)
	}
	return result
}

// ParseAllowedPorts converts a comma-separated port string (e.g. "3306,5432")
// into a uint16 slice for use in Config.AllowedPorts. Empty or blank entries
// are ignored. Returns an error for any value that is not a valid TCP port.
func ParseAllowedPorts(s string) ([]uint16, error) {
	if s == "" {
		return nil, nil
	}
	var result []uint16
	for p := range strings.SplitSeq(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.ParseUint(p, 10, 16)
		if err != nil || n == 0 {
			return nil, fmt.Errorf("netguard: invalid port %q", p)
		}
		result = append(result, uint16(n))
	}
	return result, nil
}

// ParsePrivateAllowlist converts a comma-separated CIDR string into a slice
// for use in Config.PrivateAllowlist. Empty string returns nil.
func ParsePrivateAllowlist(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	for cidr := range strings.SplitSeq(s, ",") {
		cidr = strings.TrimSpace(cidr)
		if cidr != "" {
			result = append(result, cidr)
		}
	}
	return result
}

// AllInAllowlist reports whether every IP in ips is covered by the explicit
// private allowlist. Used by the datasource service to enforce that
// tls_mode=disabled is only accepted for consciously allowlisted targets.
// Returns false when ips is empty or when any IP is not in the allowlist.
func (v *Validator) AllInAllowlist(ips []net.IP) bool {
	if len(ips) == 0 {
		return false
	}
	for _, ip := range ips {
		covered := false
		for _, allow := range v.allowlist {
			if allow.Contains(ip) {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

// Sentinel errors returned by Validate.
var (
	ErrPortNotAllowed    = errors.New("netguard: port not in allowed list")
	ErrAddressBlocked    = errors.New("netguard: address is blocked")
	ErrNoAddressResolved = errors.New("netguard: host resolved to no addresses")
	ErrInvalidHost       = errors.New("netguard: invalid host")
)
