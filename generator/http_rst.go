package generator

import (
	"context"
	"fmt"
	"log/slog"
	mathrand "math/rand/v2"
	"net"
	"time"
)

// generateHTTPPostRST implements the HTTP POST + RST scenario.
//
// It establishes a TCP connection to a target server, sends a large HTTP POST
// body, then immediately resets the connection with RST before the server can
// reply. This achieves ~99.9% TX efficiency — the only download overhead is
// the SYN-ACK (~60 bytes).
//
// Flow:
//  1. TCP handshake with remote server → ~60 bytes download (SYN-ACK)
//  2. Send: "POST <path> HTTP/1.1\r\nHost: ...\r\nContent-Length: N\r\n\r\n" + N bytes
//  3. Set SO_LINGER to {on=1, linger=0} → forces kernel to send RST on close
//  4. Close the connection
//
// Unlike the original implementation, the body is generated in 8KB chunks
// rather than allocated upfront — this prevents OOM on large jobs (100MB+).
//
// Why it works: Looks like a legitimate HTTP upload that was aborted (user
// navigated away, browser crashed, network interruption). Partial uploads
// are extremely common on the internet.
func generateHTTPPostRST(targetBytes uint64, targetHost string, targetPort int, path string) (uint64, error) {
	if targetBytes == 0 {
		return 0, nil
	}

	hostPort := net.JoinHostPort(targetHost, fmt.Sprintf("%d", targetPort))

	// Build the HTTP request headers with Content-Length so the server
	// knows how much to expect before the connection reset.
	requestLine := fmt.Sprintf(
		"POST %s HTTP/1.1\r\nHost: %s\r\nContent-Type: application/octet-stream\r\nContent-Length: %d\r\nConnection: close\r\n\r\n",
		path, targetHost, targetBytes,
	)

	// Establish TCP connection
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(context.Background(), "tcp", hostPort)
	if err != nil {
		return 0, fmt.Errorf("tcp dial to %s: %w", hostPort, err)
	}
	// No defer conn.Close() — we explicitly RST close below

	slog.Debug("http-rst: connection established",
		"target", hostPort,
		"targetBytes", targetBytes)

	// Send the HTTP request line + headers
	_, err = conn.Write([]byte(requestLine))
	if err != nil {
		conn.Close()
		return 0, fmt.Errorf("writing headers: %w", err)
	}

	var sent = uint64(len(requestLine))
	var bodySent uint64

	// Generate and send body in 8KB chunks — avoids OOM on large jobs.
	// 80% printable ASCII (like text data), 20% random binary (like compressed data).
	const chunkSize = 8192
	chunk := make([]byte, chunkSize)
	for bodySent < targetBytes {
		writeLen := chunkSize
		remaining := targetBytes - bodySent
		if uint64(chunkSize) > remaining {
			writeLen = int(remaining)
		}

		// Generate realistic content for this chunk
		for i := 0; i < writeLen; i++ {
			if mathrand.IntN(10) < 8 {
				// 80% chance: printable ASCII (space through ~)
				chunk[i] = byte(0x20 + mathrand.IntN(95))
			} else {
				// 20% chance: random binary
				chunk[i] = byte(mathrand.IntN(256))
			}
		}

		n, err := conn.Write(chunk[:writeLen])
		bodySent += uint64(n)
		sent += uint64(n)
		if err != nil {
			conn.Close()
			if bodySent > 0 {
				slog.Debug("http-rst: partial send before RST",
					"bodySent", bodySent,
					"targetBytes", targetBytes)
				return sent, nil
			}
			return sent, fmt.Errorf("writing body at offset %d: %w", bodySent, err)
		}

		// Brief pause between chunks to simulate real network writes
		if mathrand.IntN(4) == 0 {
			time.Sleep(time.Duration(1+mathrand.IntN(5)) * time.Millisecond)
		}
	}

	// Force RST by setting SO_LINGER to {on=1, linger=0}.
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		if err := tcpConn.SetLinger(0); err != nil {
			slog.Debug("http-rst: set linger failed", "error", err)
		}
		tcpConn.Close()
	}

	slog.Debug("http-rst: generation complete", "actualBytes", sent)
	return sent, nil
}
