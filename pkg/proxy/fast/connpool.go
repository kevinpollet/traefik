package fast

import (
	"bufio"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// conn is an enriched net.Conn.
type conn struct {
	net.Conn

	br *bufio.Reader

	idleAt      time.Time // the last time it was marked as idle.
	idleCh      chan struct{}
	idleTimeout time.Duration

	active bool

	broken   bool
	brokenMu sync.RWMutex
}

func (c *conn) Read(p []byte) (int, error) {
	return c.br.Read(p)
}

func (c *conn) isExpired() bool {
	expTime := c.idleAt.Add(c.idleTimeout)
	return c.idleTimeout > 0 && time.Now().After(expTime)
}

func (c *conn) isBroken() bool {
	//c.brokenMu.RLock()
	//defer c.brokenMu.RUnlock()
	return c.broken
}

func (c *conn) markAsActive() {
	c.active = true
}

func (c *conn) markAsIdle() {
	select {
	case c.idleCh <- struct{}{}:
		c.idleAt = time.Now()
		c.active = false

	default:
		// Nothing to do the connection is already marked as idle.
	}
}

func (c *conn) readLoop() {
	for {
		<-c.idleCh
		fmt.Println("Before Peek")
		_, err := c.br.Peek(1)
		fmt.Println("Peek", err)
		if err != nil {
			//c.brokenMu.Lock()
			c.broken = true
			//c.brokenMu.Unlock()
			return
		}
	}
}

// connPool is a net.Conn pool implementation using channels.
type connPool struct {
	dialer          func() (net.Conn, error)
	idleConns       chan *conn
	idleConnTimeout time.Duration
	ticker          *time.Ticker
	doneCh          chan struct{}
	readerPool      pool[*bufio.Reader]
}

// newConnPool creates a new connPool.
func newConnPool(maxIdleConn int, idleConnTimeout time.Duration, dialer func() (net.Conn, error)) *connPool {
	c := &connPool{
		dialer:          dialer,
		idleConns:       make(chan *conn, maxIdleConn),
		idleConnTimeout: idleConnTimeout,
		doneCh:          make(chan struct{}),
	}

	if idleConnTimeout > 0 {
		c.ticker = time.NewTicker(c.idleConnTimeout / 2)
		go func() {
			for {
				select {
				case <-c.ticker.C:
					c.cleanIdleConns()
				case <-c.doneCh:
					return
				}
			}
		}()
	}

	return c
}

// Close closes stop the cleanIdleConns goroutine.
func (c *connPool) Close() {
	if c.idleConnTimeout > 0 {
		close(c.doneCh)
		c.ticker.Stop()
	}
}

// AcquireConn returns an idle net.Conn from the pool.
func (c *connPool) AcquireConn() (*conn, error) {
	for {
		co, err := c.acquireConn()
		if err != nil {
			return nil, err
		}

		if !co.isExpired() && !co.isBroken() {
			co.markAsActive()
			return co, nil
		}

		// As the acquired conn is expired or closed we can close it
		// without putting it again into the pool.
		if err := co.Close(); err != nil {
			log.Debug().
				Err(err).
				Msg("Unexpected error while releasing the connection")
		}
	}
}

// ReleaseConn releases the given net.Conn to the pool.
func (c *connPool) ReleaseConn(co *conn) {
	co.markAsIdle()
	c.releaseConn(co)
}

// cleanIdleConns is a routine cleaning the expired connections at a regular basis.
func (c *connPool) cleanIdleConns() {
	for {
		select {
		case co := <-c.idleConns:
			if !co.isExpired() && !co.isBroken() {
				c.releaseConn(co)
				return
			}

			if err := co.Close(); err != nil {
				log.Debug().
					Err(err).
					Msg("Unexpected error while releasing the connection")
			}

		default:
			return
		}
	}
}

func (c *connPool) acquireConn() (*conn, error) {
	select {
	case co := <-c.idleConns:
		return co, nil

	default:
		errCh := make(chan error, 1)
		go c.askForNewConn(errCh)

		select {
		case co := <-c.idleConns:
			return co, nil

		case err := <-errCh:
			return nil, err
		}
	}
}

func (c *connPool) releaseConn(co *conn) {
	select {
	case c.idleConns <- co:

	// Hitting the default case means that we have reached the maximum number of idle
	// connections, so we can close it.
	default:
		if err := co.Close(); err != nil {
			log.Debug().
				Err(err).
				Msg("Unexpected error while releasing the connection")
		}
	}
}

func (c *connPool) askForNewConn(errCh chan<- error) {
	co, err := c.dialer()
	if err != nil {
		errCh <- fmt.Errorf("creating conn: %w", err)
		return
	}

	newConn := &conn{
		Conn:        co,
		br:          bufio.NewReaderSize(co, bufioSize),
		idleAt:      time.Now(),
		idleTimeout: c.idleConnTimeout,
		idleCh:      make(chan struct{}, 1),
	}
	go newConn.readLoop()

	c.releaseConn(newConn)
}
