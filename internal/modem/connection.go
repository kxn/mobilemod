// Package modem isolates serial transport and correlates JSON-line requests.
package modem

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"unicode/utf8"

	"github.com/kxn/mobilemod/internal/domain"
)

var ErrDisconnected = errors.New("module disconnected; a write may have reached the module")

type waiter struct {
	ch    chan domain.Envelope
	final bool
}
type Connection struct {
	port    io.ReadWriteCloser
	writeMu sync.Mutex
	mu      sync.Mutex
	waiters map[string]waiter
	done    chan struct{}
	once    sync.Once
	OnEvent func(domain.Envelope)
}

func NewConnection(port io.ReadWriteCloser) *Connection {
	return &Connection{port: port, waiters: map[string]waiter{}, done: make(chan struct{})}
}
func (c *Connection) Close()                { c.once.Do(func() { close(c.done); c.port.Close() }) }
func (c *Connection) Done() <-chan struct{} { return c.done }
func (c *Connection) Write(request any) error {
	b, err := json.Marshal(request)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-c.done:
		return ErrDisconnected
	default:
	}
	for len(b) > 0 {
		n, e := c.port.Write(b)
		if e != nil {
			c.Close()
			return ErrDisconnected
		}
		if n == 0 {
			c.Close()
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
func (c *Connection) Call(ctx context.Context, request map[string]any, final bool) (domain.Envelope, error) {
	if err := ctx.Err(); err != nil {
		return domain.Envelope{}, err
	}
	// Serial writes can block as well as reads. Closing the transport releases
	// both on cancellation, including a writer waiting behind an ACK.
	stop := context.AfterFunc(ctx, c.Close)
	defer stop()
	id, _ := request["id"].(string)
	if id == "" {
		id = rand.Text()
		request["id"] = id
	}
	w := waiter{make(chan domain.Envelope, 1), final}
	c.mu.Lock()
	if _, ok := c.waiters[id]; ok {
		c.mu.Unlock()
		return domain.Envelope{}, errors.New("request already in flight")
	}
	c.waiters[id] = w
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.waiters, id); c.mu.Unlock() }()
	if err := c.Write(request); err != nil {
		return domain.Envelope{}, err
	}
	select {
	case e := <-w.ch:
		if e.Event == "error" {
			return e, errors.New("module rejected request: " + e.Error)
		}
		return e, nil
	case <-ctx.Done():
		return domain.Envelope{}, ctx.Err()
	case <-c.done:
		return domain.Envelope{}, ErrDisconnected
	}
}
func (c *Connection) ReadLoop() error {
	defer c.Close()
	buffer := make([]byte, 0, 4096)
	chunk := make([]byte, 4096)
	for {
		n, err := c.port.Read(chunk)
		if n > 0 {
			buffer = append(buffer, chunk[:n]...)
		}
		if len(buffer) > 256*1024 {
			return errors.New("module frame exceeds limit")
		}
		for {
			i := bytes.IndexByte(buffer, '\n')
			if i < 0 {
				break
			}
			line := bytes.TrimSpace(buffer[:i])
			buffer = buffer[i+1:]
			var e domain.Envelope
			if len(line) == 0 {
				continue
			}
			if !utf8.Valid(line) {
				return errors.New("invalid UTF-8 module frame")
			}
			if json.Unmarshal(line, &e) != nil {
				return errors.New("invalid module JSON frame")
			}
			c.mu.Lock()
			w, found := c.waiters[e.ID]
			c.mu.Unlock()
			if found && (!w.final || e.Event == "sms_result" || e.Event == "error") {
				select {
				case w.ch <- e:
				default:
				}
			}
			if e.Event == "sms_received" || e.Event == "mms_received" || e.Event == "overflow" {
				if c.OnEvent != nil {
					c.OnEvent(e)
				}
			}
		}
		if err != nil {
			return err
		}
		select {
		case <-c.done:
			return nil
		default:
		}
	}
}
