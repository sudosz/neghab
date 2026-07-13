package generator

import (
	"fmt"
	"log/slog"
	mathrand "math/rand/v2"
	"net"
	"time"
)

// generateUDP implements the UDP Dead-End scenario.
//
// It sends UDP packets to a non-responsive IP (e.g., an unused address on the
// local subnet) with QUIC-like headers and random payloads. Because the target
// does not respond, zero download bytes are generated — achieving ~100% TX
// efficiency.
//
// The scenario mimics QUIC/DTLS traffic:
//   - Packet sizes vary between 800–1500 bytes (typical QUIC Initial packets)
//   - First byte uses 0xC0 (QUIC long header) or random value
//   - Payload is filled with cryptographically random bytes
//   - Packets are sent in bursts of 5–20 packets with pauses of 20–200ms
func generateUDP(targetBytes uint64, targetIP string, targetPort int) (uint64, error) {
	if targetBytes == 0 {
		return 0, nil
	}

	// Resolve target address
	addr := &net.UDPAddr{
		IP:   net.ParseIP(targetIP),
		Port: targetPort,
	}
	if addr.IP == nil {
		return 0, fmt.Errorf("invalid target IP: %s", targetIP)
	}

	// Dial UDP (connection setup does not actually send packets)
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return 0, fmt.Errorf("udp dial: %w", err)
	}
	defer conn.Close()

	var sent uint64
	// Typical QUIC/DTLS packet sizes
	packetSizes := []int{1200, 1300, 1400, 1100, 800, 900, 1000, 1280, 1350, 1450}

	slog.Debug("udp: starting generation",
		"target", fmt.Sprintf("%s:%d", targetIP, targetPort),
		"targetBytes", targetBytes)

	for sent < targetBytes {
		// Select a random packet size from the pool
		size := packetSizes[mathrand.IntN(len(packetSizes))]

		// Clamp to remaining bytes
		remaining := targetBytes - sent
		if uint64(size) > remaining {
			size = int(remaining)
		}

		// Build payload with QUIC-like header
		payload := make([]byte, size)

		// First byte: 50% chance QUIC long header (0xC0), 50% random
		// QUIC long header packets start with the two most significant bits set (0xC0).
		// This makes the traffic look like legitimate QUIC Initial packets.
		if mathrand.IntN(2) == 0 {
			payload[0] = 0xC0
		} else {
			payload[0] = byte(mathrand.IntN(256))
		}

		// Fill the rest with fast pseudo-random bytes.
		// For traffic generation, math/rand/v2 is sufficient — we don't
		// need cryptographic entropy for payload bytes.
		for j := 1; j < len(payload); j++ {
			payload[j] = byte(mathrand.IntN(256))
		}

		// Send the packet
		n, err := conn.Write(payload)
		if err != nil {
			// Write may fail if the target is unreachable or the socket is closed.
			// This is expected for the "dead-end" approach — we log and continue.
			slog.Debug("udp: write error (expected for dead-end target)",
				"error", err,
				"sent", sent)
			// Short pause before retrying
			time.Sleep(10 * time.Millisecond)
			continue
		}

		sent += uint64(n)

		// Burst pattern: occasionally pause to avoid saturating the socket.
		if mathrand.IntN(200) == 0 {
			time.Sleep(5 * time.Millisecond)
		}
	}

	slog.Debug("udp: generation complete", "actualBytes", sent)
	return sent, nil
}
