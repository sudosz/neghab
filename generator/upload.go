package generator

import (
	"fmt"
	"log/slog"
	mathrand "math/rand/v2"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// uploadTarget pairs a server address with its dedicated connection pool
// and URL path. Each target can have its own endpoint (e.g., one server
// uses /speedtest/upload.php while another uses /upload).
type uploadTarget struct {
	hostPort string
	path     string
	pool     *connPool
}

// generateHTTPUpload fans out targetBytes across N parallel HTTP POST streams,
// distributed round-robin across multiple upload targets. Each stream gets
// its own connection from the target's dedicated pool and uses that target's
// URL path.
//
// When only one target is present (single-server mode), all streams hit the
// same server — this is the backward-compatible default.
func generateHTTPUpload(targetBytes uint64, targets []uploadTarget, bufPool *sync.Pool, streams int) (uint64, error) {
	if targetBytes == 0 {
		return 0, nil
	}
	if len(targets) == 0 {
		return 0, fmt.Errorf("upload: no targets configured")
	}

	// Dynamic stream count: don't create streams with tiny payloads.
	const minChunkBytes = 256 * 1024 // 256KB
	const maxStreams = 32            // safety cap
	effective := streams
	if effective < 1 {
		effective = 1
	}
	if effective > maxStreams {
		effective = maxStreams
	}
	maxByData := int(targetBytes / minChunkBytes)
	if maxByData < 1 {
		maxByData = 1
	}
	if effective > maxByData {
		effective = maxByData
	}

	if effective < streams {
		slog.Debug("http-upload: streams clamped",
			"configured", streams,
			"effective", effective,
			"targetBytes", targetBytes)
	}

	// Single stream — skip goroutine overhead.
	if effective <= 1 {
		return uploadStream(targetBytes, targets[0], bufPool)
	}

	perStream := targetBytes / uint64(effective)
	remainder := targetBytes % uint64(effective)

	slog.Debug("http-upload: fan-out",
		"targetBytes", targetBytes,
		"streams", effective,
		"perStream", perStream,
		"targets", len(targets))

	var totalSent atomic.Uint64
	var wg sync.WaitGroup

	for i := 0; i < effective; i++ {
		chunk := perStream
		if i == effective-1 {
			chunk += remainder // last stream gets the leftover bytes
		}
		target := targets[i%len(targets)] // round-robin across targets

		wg.Add(1)
		go func(streamID int, bytes uint64, t uploadTarget) {
			defer wg.Done()
			sent, err := uploadStream(bytes, t, bufPool)
			totalSent.Add(sent)
			if err != nil {
				slog.Debug("http-upload: stream failed",
					"streamID", streamID,
					"target", t.hostPort,
					"path", t.path,
					"targetBytes", bytes,
					"sent", sent,
					"error", err)
			}
		}(i, chunk, target)
	}

	wg.Wait()
	final := totalSent.Load()

	if final > 0 {
		slog.Debug("http-upload: complete",
			"streams", effective,
			"actualBytes", final)
		return final, nil
	}

	return 0, fmt.Errorf("upload: all %d streams failed across %d target(s)", effective, len(targets))
}

// uploadStream performs a single HTTP POST upload on one connection to a
// specific target. Each stream is independent — gets its own connection from
// the target's pool, writes the body, drains the response, and returns the
// connection (or closes it on error).
func uploadStream(targetBytes uint64, target uploadTarget, bufPool *sync.Pool) (uint64, error) {
	conn, err := target.pool.get()
	if err != nil {
		return 0, fmt.Errorf("upload pool get (%s): %w", target.hostPort, err)
	}

	// Set TCP send buffer to 1MB for better throughput
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.SetWriteBuffer(1_048_576) //nolint:errcheck
		_ = tcpConn.SetReadBuffer(65536)      //nolint:errcheck
	}

	requestLine := fmt.Sprintf(
		"POST %s HTTP/1.1\r\nHost: %s\r\nContent-Type: application/octet-stream\r\nContent-Length: %d\r\nConnection: keep-alive\r\nUser-Agent: Mozilla/5.0 (X11; Linux x86_64) Neghab/1.0\r\nAccept: */*\r\n\r\n",
		target.path, target.hostPort, targetBytes,
	)

	_, err = conn.Write([]byte(requestLine))
	if err != nil {
		conn.Close()
		return 0, fmt.Errorf("upload headers (%s): %w", target.hostPort, err)
	}

	// Send body in configurable-size chunks from the pool
	buf := bufPool.Get().([]byte) //nolint:staticcheck,errcheck
	defer bufPool.Put(buf)        //nolint:staticcheck

	var bodySent uint64
	for bodySent < targetBytes {
		remaining := targetBytes - bodySent
		writeLen := len(buf)
		if uint64(writeLen) > remaining {
			writeLen = int(remaining)
		}

		// Freshen first 256 bytes for entropy
		for j := 0; j < 256; j++ {
			buf[j] = byte(mathrand.IntN(256))
		}

		n, err := conn.Write(buf[:writeLen])
		bodySent += uint64(n)
		if err != nil {
			conn.Close()
			if bodySent > 0 {
				return bodySent, nil
			}
			return bodySent, fmt.Errorf("upload body write (%s): %w", target.hostPort, err)
		}
	}

	drainResponse(conn)
	target.pool.put(conn)

	return bodySent, nil
}

// drainBufPool reuses 64KB buffers for response draining across goroutines.
// Uses *[65536]byte (pointer type) for zero-allocation sync.Pool operation.
var drainBufPool = sync.Pool{
	New: func() any { return new([65536]byte) },
}

// drainResponse reads and discards the HTTP response to prevent TCP buffer
// blocking and allow the connection to be reused for keep-alive.
// Uses a pooled buffer to avoid per-call allocation on the hot path.
func drainResponse(conn net.Conn) {
	arr := drainBufPool.Get().(*[65536]byte) //nolint:errcheck
	defer drainBufPool.Put(arr)
	buf := arr[:]

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second)) //nolint:errcheck
	var total int
	for {
		n, err := conn.Read(buf)
		total += n
		if err != nil {
			break
		}
		_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second)) //nolint:errcheck
	}
	if total > 0 {
		slog.Debug("http: response drained", "bytes", total)
	}
}
