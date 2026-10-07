package app

import (
	"net"
	"sync"
)

// Bound sockets before net/http allocates per-connection goroutines/buffers.
type limitedListener struct {
	net.Listener
	slots chan struct{}
}
type limitedConnection struct {
	net.Conn
	slots chan struct{}
	once  sync.Once
}

func (c *limitedConnection) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { <-c.slots })
	return err
}
func (l *limitedListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
			return &limitedConnection{Conn: conn, slots: l.slots}, nil
		default:
			conn.Close()
		}
	}
}
