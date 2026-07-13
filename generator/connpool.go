package generator

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

// connPool maintains a pool of persistent TCP connections to a single target.
// Workers grab a connection, use it for one HTTP request/response cycle,
// then return it. This eliminates the TCP handshake overhead per job and
// keeps congestion windows warm.
//
// Dead connections are detected naturally when writes fail — the caller
// should Close() the connection instead of returning it to the pool.
type connPool struct {
	mu          sync.Mutex
	conns       []*pooledConn
	addr        string
	maxIdle     int
	dialTimeout time.Duration
	idleTimeout time.Duration
	closed      bool
}

type pooledConn struct {
	conn     net.Conn
	lastUsed time.Time
}

// newConnPool creates a connection pool for the given address.
// maxIdle connections are kept alive between jobs.
func newConnPool(addr string, maxIdle int) *connPool {
	return &connPool{
		addr:        addr,
		maxIdle:     maxIdle,
		dialTimeout: 10 * time.Second,
		idleTimeout: 30 * time.Second,
	}
}

// get returns a connection from the pool or dials a new one.
// The lock is released before dialing to avoid blocking other workers.
func (p *connPool) get() (net.Conn, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, fmt.Errorf("connection pool closed")
	}

	now := time.Now()
	for len(p.conns) > 0 {
		pc := p.conns[len(p.conns)-1]
		p.conns = p.conns[:len(p.conns)-1]

		if now.Sub(pc.lastUsed) > p.idleTimeout {
			pc.conn.Close()
			continue
		}

		conn := pc.conn
		p.mu.Unlock()
		return conn, nil
	}
	p.mu.Unlock()

	return p.dial()
}

// put returns a connection to the pool, or closes it if the pool is full.
func (p *connPool) put(conn net.Conn) {
	if conn == nil {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed || len(p.conns) >= p.maxIdle {
		conn.Close()
		return
	}

	p.conns = append(p.conns, &pooledConn{
		conn:     conn,
		lastUsed: time.Now(),
	})
}

// close closes all connections in the pool.
func (p *connPool) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	for _, pc := range p.conns {
		pc.conn.Close()
	}
	p.conns = nil
}

// dial creates a new TCP connection to the target.
func (p *connPool) dial() (net.Conn, error) {
	dialer := net.Dialer{Timeout: p.dialTimeout}
	return dialer.DialContext(context.Background(), "tcp", p.addr)
}

// prewarm pre-dials n connections and adds them to the pool.
// This eliminates the TCP handshake cost for the first n jobs.
// Call before any goroutines start using the pool.
func (p *connPool) prewarm(n int) {
	for i := 0; i < n; i++ {
		conn, err := p.dial()
		if err != nil {
			continue // retry remaining — one slow dial shouldn't block all pre-warming
		}
		p.put(conn)
	}
}
