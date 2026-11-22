package resolver

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestFakeIPReason(t *testing.T) {
	for _, tc := range []struct {
		ip   string
		want string
	}{
		{"198.18.0.1", "198.18.0.0/15"},
		{"198.19.255.254", "198.18.0.0/15"},
		{"fc00::1", "fc00::/18"},
		{"203.0.113.7", ""},
		{"2001:db8::7", ""},
	} {
		if got := FakeIPReason(netip.MustParseAddr(tc.ip)); got != tc.want {
			t.Errorf("FakeIPReason(%s) = %q want %q", tc.ip, got, tc.want)
		}
	}
}

func TestNewSpec(t *testing.T) {
	for _, spec := range []string{"", "system", "udp:1.1.1.1:53", "udp://1.1.1.1:53", "doh:https://dns.example/dns-query", "https://dns.example/q"} {
		if _, err := New(spec); err != nil {
			t.Errorf("New(%q): %v", spec, err)
		}
	}
	for _, spec := range []string{"udp:1.1.1.1", "doh:http://insecure", "whatever"} {
		if _, err := New(spec); err == nil {
			t.Errorf("New(%q) should fail", spec)
		}
	}
}

// dnsResponse builds a response to query carrying the given records. It is
// also called from a server goroutine, so it returns errors instead of using
// testing.T.
func dnsResponse(query []byte, ipv4, ipv6 []netip.Addr, ttl uint32) ([]byte, error) {
	var p dnsmessage.Parser
	h, err := p.Start(query)
	if err != nil {
		return nil, err
	}
	q, err := p.Question()
	if err != nil {
		return nil, err
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID: h.ID, Response: true, RecursionAvailable: true,
	})
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(q); err != nil {
		return nil, err
	}
	if err := b.StartAnswers(); err != nil {
		return nil, err
	}
	for _, ip := range ipv4 {
		err := b.AResource(dnsmessage.ResourceHeader{
			Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: ttl,
		}, dnsmessage.AResource{A: ip.As4()})
		if err != nil {
			return nil, err
		}
	}
	for _, ip := range ipv6 {
		err := b.AAAAResource(dnsmessage.ResourceHeader{
			Name: q.Name, Type: dnsmessage.TypeAAAA, Class: dnsmessage.ClassINET, TTL: ttl,
		}, dnsmessage.AAAAResource{AAAA: ip.As16()})
		if err != nil {
			return nil, err
		}
	}
	return b.Finish()
}

// udpDNSServer answers A/AAAA queries with the supplied records.
func udpDNSServer(t *testing.T, v4, v6 []netip.Addr, ttl uint32) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 64<<10)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			query := append([]byte(nil), buf[:n]...)
			var p dnsmessage.Parser
			if _, err := p.Start(query); err != nil {
				continue
			}
			q, err := p.Question()
			if err != nil {
				continue
			}
			var resp []byte
			switch q.Type {
			case dnsmessage.TypeA:
				resp, err = dnsResponse(query, v4, nil, ttl)
			case dnsmessage.TypeAAAA:
				resp, err = dnsResponse(query, nil, v6, ttl)
			default:
				resp, err = dnsResponse(query, v4, v6, ttl)
			}
			if err != nil {
				continue
			}
			pc.WriteTo(resp, addr)
		}
	}()
	return pc.LocalAddr().String()
}

func TestUDPResolver(t *testing.T) {
	server := udpDNSServer(t, []netip.Addr{netip.MustParseAddr("203.0.113.7")}, []netip.Addr{netip.MustParseAddr("2001:db8::7")}, 600)
	r, err := New("udp:" + server)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := r.Lookup(ctx, "vpn.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Answers) != 2 {
		t.Fatalf("answers = %v", res.Answers)
	}
	if res.Answers[0].IP.String() != "203.0.113.7" || res.Answers[1].IP.String() != "2001:db8::7" {
		t.Fatalf("unexpected answers: %v", res.Answers)
	}
	if res.TTL != 600*time.Second {
		t.Fatalf("ttl = %v want 600s", res.TTL)
	}
	if res.Server != "udp:"+server {
		t.Fatalf("server = %q", res.Server)
	}
}

func TestExchangeTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var lenBuf [2]byte
		if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
			return
		}
		query := make([]byte, binary.BigEndian.Uint16(lenBuf[:]))
		if _, err := io.ReadFull(conn, query); err != nil {
			return
		}
		resp, err := dnsResponse(query, []netip.Addr{netip.MustParseAddr("203.0.113.7")}, nil, 60)
		if err != nil {
			return
		}
		binary.BigEndian.PutUint16(lenBuf[:], uint16(len(resp)))
		conn.Write(append(lenBuf[:], resp...))
	}()

	query, err := buildQuery("vpn.example", dnsmessage.TypeA)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	buf, _, err := exchange(ctx, "tcp", ln.Addr().String(), query)
	if err != nil {
		t.Fatal(err)
	}
	answers, _, err := parseResponse(buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(answers) != 1 || answers[0].IP.String() != "203.0.113.7" {
		t.Fatalf("answers = %v", answers)
	}
}

func TestDoHResolver(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p dnsmessage.Parser
		if _, err := p.Start(body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		q, err := p.Question()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var v4, v6 []netip.Addr
		if q.Type == dnsmessage.TypeA {
			v4 = []netip.Addr{netip.MustParseAddr("198.51.100.9")}
		} else {
			v6 = []netip.Addr{netip.MustParseAddr("2001:db8::9")}
		}
		resp, err := dnsResponse(body, v4, v6, 120)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		w.Write(resp)
	}))
	defer srv.Close()

	r, err := New("doh:" + srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	dr := r.(*dohResolver)
	dr.http = srv.Client()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := dr.Lookup(ctx, "vpn.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Answers) != 2 {
		t.Fatalf("answers = %v", res.Answers)
	}
	if res.TTL != 120*time.Second {
		t.Fatalf("ttl = %v want 120s", res.TTL)
	}
}

func TestParseResponseSkipsOtherRecords(t *testing.T) {
	query, err := buildQuery("vpn.example", dnsmessage.TypeA)
	if err != nil {
		t.Fatal(err)
	}
	var p dnsmessage.Parser
	h, err := p.Start(query)
	if err != nil {
		t.Fatal(err)
	}
	q, _ := p.Question()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true})
	b.EnableCompression()
	b.StartQuestions()
	b.Question(q)
	b.StartAnswers()
	// CNAME first, then the A record it points at.
	if err := b.CNAMEResource(dnsmessage.ResourceHeader{
		Name: q.Name, Type: dnsmessage.TypeCNAME, Class: dnsmessage.ClassINET, TTL: 300,
	}, dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName("real.example.")}); err != nil {
		t.Fatal(err)
	}
	if err := b.AResource(dnsmessage.ResourceHeader{
		Name: dnsmessage.MustNewName("real.example."), Type: dnsmessage.TypeA,
		Class: dnsmessage.ClassINET, TTL: 60,
	}, dnsmessage.AResource{A: [4]byte{203, 0, 113, 5}}); err != nil {
		t.Fatal(err)
	}
	msg, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	answers, ttl, err := parseResponse(msg)
	if err != nil {
		t.Fatal(err)
	}
	if len(answers) != 1 || answers[0].IP.String() != "203.0.113.5" {
		t.Fatalf("answers = %v", answers)
	}
	if ttl != 60*time.Second {
		t.Fatalf("ttl = %v", ttl)
	}
}
