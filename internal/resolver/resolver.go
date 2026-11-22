// Package resolver resolves peer endpoint hostnames with a pluggable backend.
//
// The default is the system resolver, which is what wgtun has always used.
// Two explicit backends exist for hosts whose DNS is intercepted by the
// router (OpenWrt fake-IP, DNS hijacking): plain UDP to a chosen server and
// DNS-over-HTTPS. Both return the answer TTL, which the system resolver API
// hides, and both let the daemon log where an answer actually came from.
package resolver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Answer is one address from a DNS response.
type Answer struct {
	IP  netip.Addr
	TTL time.Duration
}

// Result is the outcome of one lookup.
type Result struct {
	Answers []Answer
	TTL     time.Duration // smallest answer TTL; 0 when unknown (system)
	Server  string        // human-readable source: "system", "udp:1.1.1.1:53", "doh:..."
}

// Resolver looks up A and AAAA records for a hostname.
type Resolver interface {
	Lookup(ctx context.Context, host string) (Result, error)
	String() string
}

// New builds a resolver from a spec:
//
//	""  or "system"                        system resolver
//	"udp:1.1.1.1:53" / "udp://1.1.1.1:53"  plain DNS to that server
//	"doh:https://dns.alidns.com/dns-query" DNS-over-HTTPS (RFC 8484)
//	"https://..."                          same as doh:
//
// An empty spec never errors; unknown specs do.
func New(spec string) (Resolver, error) {
	s := strings.TrimSpace(spec)
	switch {
	case s == "" || strings.EqualFold(s, "system"):
		return systemResolver{}, nil
	case strings.HasPrefix(s, "udp:"):
		addr := strings.TrimPrefix(s, "udp:")
		addr = strings.TrimPrefix(addr, "//")
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return nil, fmt.Errorf("resolver %q: want udp:host:port: %v", spec, err)
		}
		return &dnsResolver{network: "udp", server: addr, label: "udp:" + addr}, nil
	case strings.HasPrefix(s, "doh:"):
		return newDoH(strings.TrimPrefix(s, "doh:"))
	case strings.HasPrefix(s, "https://"):
		return newDoH(s)
	default:
		return nil, fmt.Errorf("resolver %q: want system, udp:host:port, doh:https://... or https://...", spec)
	}
}

// FakeIPReason reports the fake-IP range an address falls into, or "" when it
// is not a well-known fake-IP address.
//
// 198.18.0.0/15 is the RFC 2544 benchmarking range, repurposed by Clash,
// Surge and sing-box as their default fake-IP pool; fc00::/18 is the matching
// fake-ip6 default in Clash. A router answering inside these ranges is not
// telling us where the peer really is, so DDNS tracking cannot see an address
// change — the caller should warn and offer a DoH/UDP resolver instead.
func FakeIPReason(ip netip.Addr) string {
	for _, p := range fakeRanges {
		if p.Contains(ip) {
			return p.String()
		}
	}
	return ""
}

var fakeRanges = []netip.Prefix{
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("fc00::/18"),
}

// systemResolver uses the platform resolver (mDNSResponder on macOS,
// /etc/resolv.conf or systemd-resolved on Linux).
type systemResolver struct{}

func (systemResolver) String() string { return "system" }

func (systemResolver) Lookup(ctx context.Context, host string) (Result, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return Result{}, err
	}
	res := Result{Server: "system"}
	seen := map[netip.Addr]bool{}
	for _, a := range addrs {
		ip, ok := netip.AddrFromSlice(a.IP)
		if !ok || seen[ip] {
			continue
		}
		seen[ip] = true
		res.Answers = append(res.Answers, Answer{IP: ip}) // TTL not exposed
	}
	if len(res.Answers) == 0 {
		return Result{}, fmt.Errorf("no addresses for %s", host)
	}
	return res, nil
}

// dnsResolver speaks the DNS wire protocol over UDP (falling back to TCP when
// the response is truncated).
type dnsResolver struct {
	network string // "udp"
	server  string
	label   string
}

func (r *dnsResolver) String() string { return r.label }

func (r *dnsResolver) Lookup(ctx context.Context, host string) (Result, error) {
	a, aTTL, err := r.query(ctx, host, dnsmessage.TypeA)
	if err != nil {
		return Result{}, err
	}
	aaaa, aaaaTTL, err := r.query(ctx, host, dnsmessage.TypeAAAA)
	if err != nil && !isNotFound(err) {
		return Result{}, err
	}
	res := Result{Server: r.label, TTL: minTTL(aTTL, aaaaTTL)}
	res.Answers = append(res.Answers, a...)
	res.Answers = append(res.Answers, aaaa...)
	if len(res.Answers) == 0 {
		return res, fmt.Errorf("no addresses for %s via %s", host, r.label)
	}
	return res, nil
}

func (r *dnsResolver) query(ctx context.Context, host string, qtype dnsmessage.Type) ([]Answer, time.Duration, error) {
	msg, err := buildQuery(host, qtype)
	if err != nil {
		return nil, 0, err
	}
	buf, truncated, err := exchange(ctx, "udp", r.server, msg)
	if err != nil {
		return nil, 0, err
	}
	if truncated {
		buf, _, err = exchange(ctx, "tcp", r.server, msg)
		if err != nil {
			return nil, 0, err
		}
	}
	answers, ttl, err := parseResponse(buf)
	if err != nil {
		return nil, 0, err
	}
	return answers, ttl, nil
}

// dohResolver sends RFC 8484 queries over HTTPS.
type dohResolver struct {
	url  string
	http *http.Client
}

func newDoH(url string) (Resolver, error) {
	url = strings.TrimSpace(url)
	if !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("doh resolver %q: URL must start with https://", url)
	}
	return &dohResolver{
		url:  url,
		http: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func (r *dohResolver) String() string { return "doh:" + r.url }

func (r *dohResolver) Lookup(ctx context.Context, host string) (Result, error) {
	a, aTTL, err := r.query(ctx, host, dnsmessage.TypeA)
	if err != nil {
		return Result{}, err
	}
	aaaa, aaaaTTL, err := r.query(ctx, host, dnsmessage.TypeAAAA)
	if err != nil && !isNotFound(err) {
		return Result{}, err
	}
	res := Result{Server: r.String(), TTL: minTTL(aTTL, aaaaTTL)}
	res.Answers = append(res.Answers, a...)
	res.Answers = append(res.Answers, aaaa...)
	if len(res.Answers) == 0 {
		return res, fmt.Errorf("no addresses for %s via %s", host, r.String())
	}
	return res, nil
}

func (r *dohResolver) query(ctx context.Context, host string, qtype dnsmessage.Type) ([]Answer, time.Duration, error) {
	msg, err := buildQuery(host, qtype)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(msg))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("doh %s: HTTP %s", r.url, resp.Status)
	}
	buf, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, 0, err
	}
	return parseResponse(buf)
}

// buildQuery creates a recursion-desired A or AAAA query with a random ID.
func buildQuery(host string, qtype dnsmessage.Type) ([]byte, error) {
	name := strings.TrimSuffix(host, ".")
	if name == "" {
		return nil, fmt.Errorf("empty hostname")
	}
	n, err := dnsmessage.NewName(name + ".")
	if err != nil {
		return nil, err
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:               randomID(),
		RecursionDesired: true,
	})
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(dnsmessage.Question{Name: n, Type: qtype, Class: dnsmessage.ClassINET}); err != nil {
		return nil, err
	}
	return b.Finish()
}

func randomID() uint16 {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return uint16(time.Now().UnixNano())
	}
	return binary.BigEndian.Uint16(b[:])
}

// exchange sends msg over network and returns the response. truncated reports
// the TC bit so a UDP caller can retry over TCP.
func exchange(ctx context.Context, network, server string, msg []byte) ([]byte, bool, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, network, server)
	if err != nil {
		return nil, false, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	if network == "tcp" {
		// DNS over TCP prefixes the query with a two-byte length as well.
		prefixed := make([]byte, 2+len(msg))
		binary.BigEndian.PutUint16(prefixed[:2], uint16(len(msg)))
		copy(prefixed[2:], msg)
		if _, err := conn.Write(prefixed); err != nil {
			return nil, false, err
		}
	} else if _, err := conn.Write(msg); err != nil {
		return nil, false, err
	}
	if network == "tcp" {
		var lenBuf [2]byte
		if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
			return nil, false, err
		}
		n := binary.BigEndian.Uint16(lenBuf[:])
		buf := make([]byte, n)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return nil, false, err
		}
		return buf, false, nil
	}
	buf := make([]byte, 64<<10)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, false, err
	}
	buf = buf[:n]
	// Peek at the header for the TC flag before parsing; a truncated answer
	// has no usable records anyway.
	if len(buf) >= 4 {
		flags := binary.BigEndian.Uint16(buf[2:4])
		if flags&0x0200 != 0 {
			return buf, true, nil
		}
	}
	return buf, false, nil
}

// parseResponse extracts A/AAAA answers and the smallest TTL. CNAME and other
// records are skipped.
func parseResponse(buf []byte) ([]Answer, time.Duration, error) {
	var p dnsmessage.Parser
	h, err := p.Start(buf)
	if err != nil {
		return nil, 0, err
	}
	if h.RCode != dnsmessage.RCodeSuccess {
		return nil, 0, &dnsError{rcode: h.RCode}
	}
	// The parser must consume the question section before AnswerHeader will
	// advance past it; with no questions the first Question call reports
	// ErrSectionDone and moves the parser on.
	for {
		if _, err := p.Question(); err != nil {
			if errors.Is(err, dnsmessage.ErrSectionDone) {
				break
			}
			return nil, 0, err
		}
	}
	var out []Answer
	var ttl time.Duration
	for {
		ah, err := p.AnswerHeader()
		if err != nil {
			if errors.Is(err, dnsmessage.ErrSectionDone) {
				break
			}
			return nil, 0, err
		}
		recTTL := time.Duration(ah.TTL) * time.Second
		switch ah.Type {
		case dnsmessage.TypeA:
			r, err := p.AResource()
			if err != nil {
				return nil, 0, err
			}
			out = append(out, Answer{IP: netip.AddrFrom4(r.A), TTL: recTTL})
			ttl = minTTL(ttl, recTTL)
		case dnsmessage.TypeAAAA:
			r, err := p.AAAAResource()
			if err != nil {
				return nil, 0, err
			}
			out = append(out, Answer{IP: netip.AddrFrom16(r.AAAA), TTL: recTTL})
			ttl = minTTL(ttl, recTTL)
		default:
			if err := p.SkipAnswer(); err != nil {
				return nil, 0, err
			}
		}
	}
	return out, ttl, nil
}

type dnsError struct{ rcode dnsmessage.RCode }

func (e *dnsError) Error() string { return "dns rcode " + e.rcode.String() }

// isNotFound reports whether err means "this name has no record of that type",
// which is not fatal when the other address family answered.
func isNotFound(err error) bool {
	var de *dnsError
	if errors.As(err, &de) {
		return de.rcode == dnsmessage.RCodeNameError || de.rcode == dnsmessage.RCodeSuccess
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.IsNotFound
	}
	return false
}

func minTTL(a, b time.Duration) time.Duration {
	switch {
	case a == 0:
		return b
	case b == 0:
		return a
	case a < b:
		return a
	default:
		return b
	}
}
