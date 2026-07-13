package generator

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	mathrand "math/rand/v2"
	"net"
	"strings"
	"time"
)

// Default DNS resolvers to use for the DNS query spam scenario.
var defaultDNSResolvers = []string{
	"8.8.8.8:53",        // Google
	"1.1.1.1:53",        // Cloudflare
	"9.9.9.9:53",        // Quad9
	"208.67.222.222:53", // OpenDNS
}

// generateDNSQuerySpam implements the DNS Query Spam scenario.
//
// It sends thousands of DNS queries to public resolvers with random
// subdomains. Each query looks like malware callout traffic or ad
// tracking — extremely common on the internet.
//
// Efficiency analysis:
//   - Each query: ~40 bytes UDP header + ~30-40 bytes DNS message = ~70-80 bytes TX
//   - Each response: ~60 bytes DNS NXDOMAIN response = ~60 bytes RX
//   - TX/RX efficiency: ~55-60% (not great, but queries are easy to generate
//     in large volumes, and DNS traffic is universally allowed)
//
// Optimisation: Uses persistent UDP connections per resolver to eliminate
// per-query dial overhead. Type AAAA queries return smaller NXDOMAIN responses.
func generateDNSQuerySpam(targetBytes uint64, resolvers []string) (uint64, error) {
	if targetBytes == 0 {
		return 0, nil
	}

	if len(resolvers) == 0 {
		resolvers = defaultDNSResolvers
	}

	// Random subdomain labels to use (mix of realistic and random)
	labels := []string{
		"api", "cdn", "img", "static", "analytics", "track", "metrics",
		"collect", "events", "logs", "data", "sync", "push", "ws",
		"cdn", "media", "assets", "uploads", "downloads", "auth",
	}
	tlds := []string{"com", "net", "org", "io", "co", "app", "dev", "cloud"}

	// Resolve and connect to each resolver once, reusing the connection
	// for all queries. This eliminates per-query DialContext overhead.
	type dnsConn struct {
		addr string
		conn net.Conn
	}
	var conns []dnsConn
	for _, r := range resolvers {
		if !strings.Contains(r, ":") {
			r += ":53"
		}
		dialer := net.Dialer{Timeout: 5 * time.Second}
		conn, err := dialer.DialContext(context.Background(), "udp", r)
		if err != nil {
			slog.Debug("dns: failed to connect to resolver", "resolver", r, "error", err)
			continue
		}
		conns = append(conns, dnsConn{addr: r, conn: conn})
	}
	defer func() {
		for _, c := range conns {
			c.conn.Close()
		}
	}()

	if len(conns) == 0 {
		return 0, fmt.Errorf("no DNS resolvers reachable")
	}

	// Each query is approximately 70-80 bytes TX.
	// Underestimate at 60 bytes to ensure we generate at least targetBytes.
	const bytesPerQuery = 60
	numQueries := int(targetBytes / bytesPerQuery)
	if numQueries == 0 {
		numQueries = 1
	}

	var sent int
	queryBuf := make([]byte, 512) // Max DNS message size

	slog.Debug("dns: starting query spam",
		"resolvers", resolvers,
		"queries", numQueries,
		"targetBytes", targetBytes)

	for i := 0; i < numQueries; i++ {
		dc := conns[i%len(conns)]

		// Build a random domain name
		label1 := labels[mathrand.IntN(len(labels))]
		label2 := labels[mathrand.IntN(len(labels))]
		tld := tlds[mathrand.IntN(len(tlds))]
		prefix := fmt.Sprintf("%08x", mathrand.Uint32())[:6]
		domain := fmt.Sprintf("%s-%s.%s.%s", prefix, label1, label2, tld)

		queryLen := buildDNSQuery(queryBuf, domain, uint16(i))

		_, err := dc.conn.Write(queryBuf[:queryLen])
		if err != nil {
			slog.Debug("dns: write error", "resolver", dc.addr, "error", err)
			continue
		}
		sent += queryLen

		// Brief pause every 100 queries to avoid overwhelming the resolver.
		if i > 0 && i%100 == 0 {
			time.Sleep(5 * time.Millisecond)
		}
	}

	return uint64(sent), nil
}

// buildDNSQuery builds a DNS query message for the given domain.
// Returns the total length of the query message.
//
// DNS message format:
//   - Header (12 bytes)
//   - Question section: QNAME (encoded labels) + QTYPE (2 bytes) + QCLASS (2 bytes)
func buildDNSQuery(buf []byte, domain string, id uint16) int {
	offset := 0

	// Header (12 bytes)
	binary.BigEndian.PutUint16(buf[offset:], id) // ID
	offset += 2
	binary.BigEndian.PutUint16(buf[offset:], 0x0100) // Flags: standard query, recursion desired
	offset += 2
	binary.BigEndian.PutUint16(buf[offset:], 1) // Questions: 1
	offset += 2
	binary.BigEndian.PutUint16(buf[offset:], 0) // Answer RRs: 0
	offset += 2
	binary.BigEndian.PutUint16(buf[offset:], 0) // Authority RRs: 0
	offset += 2
	binary.BigEndian.PutUint16(buf[offset:], 0) // Additional RRs: 0
	offset += 2

	// Question: QNAME (encoded domain)
	labels := strings.Split(domain, ".")
	for _, label := range labels {
		buf[offset] = byte(len(label))
		offset++
		copy(buf[offset:], label)
		offset += len(label)
	}
	buf[offset] = 0 // Root label (end of QNAME)
	offset++

	// QTYPE: AAAA (IPv6) → smaller response for NXDOMAIN than A
	binary.BigEndian.PutUint16(buf[offset:], 28) // Type AAAA
	offset += 2
	binary.BigEndian.PutUint16(buf[offset:], 1) // Class IN
	offset += 2

	return offset
}
