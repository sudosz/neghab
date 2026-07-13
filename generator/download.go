package generator

import (
	"fmt"
	"log/slog"
	"net"
	"time"
)

// generateHTTPDownload makes an HTTP GET request and reads the response body.
// This generates RX traffic on the neghab interface — when you download a file,
// the server uploads it. The generated RX feeds into the controller's ratio
// formula (desiredTX = deltaRX / ratio), amplifying TX generation and creating
// natural bidirectional traffic.
//
// Uses the same persistent connection pool as upload — connections are
// shared and reused across both scenarios.
func generateHTTPDownload(targetBytes uint64, targetIP string, targetPort int, path string, pool *connPool) (uint64, error) {
	if targetBytes == 0 {
		return 0, nil
	}

	conn, err := pool.get()
	if err != nil {
		return 0, fmt.Errorf("download pool get: %w", err)
	}

	// Large read buffer for receiving response bodies
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.SetWriteBuffer(1_048_576)
		_ = tcpConn.SetReadBuffer(1_048_576)
	}

	hostPort := net.JoinHostPort(targetIP, fmt.Sprintf("%d", targetPort))

	requestLine := fmt.Sprintf(
		"GET %s HTTP/1.1\r\nHost: %s\r\nConnection: keep-alive\r\nUser-Agent: Mozilla/5.0 (X11; Linux x86_64) Neghab/1.0\r\nAccept: */*\r\n\r\n",
		path, hostPort,
	)

	_, writeErr := conn.Write([]byte(requestLine))
	if writeErr != nil {
		conn.Close()
		return 0, fmt.Errorf("download headers: %w", writeErr)
	}

	slog.Debug("http-download: started",
		"target", hostPort,
		"targetBytes", targetBytes)

	// Read response body. Each byte read is an RX byte on the interface.
	// The server's response includes HTTP headers first (~200-500 bytes)
	// followed by the actual body — all count as RX.
	var bytesRead uint64
	buf := make([]byte, 65536)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for bytesRead < targetBytes {
		n, readErr := conn.Read(buf)
		bytesRead += uint64(n)
		if readErr != nil {
			break
		}
		_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	}

	// Drain remaining response, then return the connection to the pool.
	// The drainResponse call reads any leftover data so the connection is
	// clean for the next upload job. Since both upload and download share
	// the same pool, keeping connections alive benefits both scenarios.
	drainResponse(conn)
	pool.put(conn)

	slog.Debug("http-download: complete",
		"bytesRead", bytesRead)
	return bytesRead, nil //nolint:nilerr
}
