// Package aatls runs TLS for an Android Auto session, where TLS records do
// not travel on a socket of their own.
package aatls

import (
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"time"
)

// Engine is the encrypted layer of a session. The phone is the TLS server and
// the car is the client. TLS records do not travel on a socket of their own:
// during the handshake they ride inside control messages, and afterwards
// each frame's payload is encrypted and decrypted on its own. So the TLS
// engine runs over an in-memory connection that this type fills and drains.
type Engine struct {
	mem *memConn
	tls *tls.Conn
}

// NewServer returns the phone side, presenting cert to the car.
func NewServer(cert tls.Certificate) *Engine {
	mem := &memConn{}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		MaxVersion:   tls.VersionTLS12,
		// The car presents a certificate; dexd has no reason to check it.
		ClientAuth:             tls.RequestClientCert,
		SessionTicketsDisabled: true,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_RSA_WITH_AES_256_CBC_SHA,
		},
	}
	return &Engine{mem: mem, tls: tls.Server(mem, cfg)}
}

// NewClient returns the car side. It exists for the simulated car used in
// tests and in the aacar tool.
func NewClient(cfg *tls.Config) *Engine {
	mem := &memConn{}
	return &Engine{mem: mem, tls: tls.Client(mem, cfg)}
}

// Handshake runs the TLS handshake. send delivers handshake bytes to the car
// and recv returns the next handshake bytes from it.
func (s *Engine) Handshake(send func([]byte) error, recv func() ([]byte, error)) error {
	s.mem.send, s.mem.recv = send, recv
	err := s.tls.Handshake()
	if err == nil {
		err = s.mem.flush()
	}
	s.mem.send, s.mem.recv = nil, nil
	return err
}

// Encrypt turns one frame's plaintext into the bytes to put on the wire.
func (s *Engine) Encrypt(plain []byte) ([]byte, error) {
	if _, err := s.tls.Write(plain); err != nil {
		return nil, err
	}
	return s.mem.takeOut(), nil
}

// Decrypt turns one frame's payload, holding one or more TLS records, back
// into plaintext.
func (s *Engine) Decrypt(cipher []byte) ([]byte, error) {
	s.mem.in = append(s.mem.in, cipher...)
	var out []byte
	buf := make([]byte, 17000)
	for {
		n, err := s.tls.Read(buf)
		out = append(out, buf[:n]...)
		if errors.Is(err, errDrained) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
	}
}

// errDrained tells the TLS engine there is nothing more to read right now.
// It reports itself as temporary so the engine stays usable afterwards.
var errDrained net.Error = drained{}

type drained struct{}

func (drained) Error() string   { return "aatls: no more encrypted input" }
func (drained) Timeout() bool   { return true }
func (drained) Temporary() bool { return true }

// memConn is the in-memory connection under the TLS engine. One goroutine
// may decrypt while another encrypts: in belongs to the reader, and out is
// guarded because the engine can also write (an alert) while reading.
type memConn struct {
	in    []byte
	outMu sync.Mutex
	out   []byte
	send  func([]byte) error     // set only during the handshake
	recv  func() ([]byte, error) // set only during the handshake
}

func (c *memConn) flush() error {
	b := c.takeOut()
	if len(b) == 0 || c.send == nil {
		return nil
	}
	return c.send(b)
}

func (c *memConn) takeOut() []byte {
	c.outMu.Lock()
	defer c.outMu.Unlock()
	b := c.out
	c.out = nil
	return b
}

func (c *memConn) Read(p []byte) (int, error) {
	for len(c.in) == 0 {
		if c.recv == nil {
			return 0, errDrained
		}
		// The engine wants input: first hand over what it has produced.
		if err := c.flush(); err != nil {
			return 0, err
		}
		b, err := c.recv()
		if err != nil {
			return 0, err
		}
		c.in = b
	}
	n := copy(p, c.in)
	c.in = c.in[n:]
	return n, nil
}

func (c *memConn) Write(p []byte) (int, error) {
	c.outMu.Lock()
	c.out = append(c.out, p...)
	c.outMu.Unlock()
	return len(p), nil
}

func (c *memConn) Close() error                     { return nil }
func (c *memConn) LocalAddr() net.Addr              { return memAddr{} }
func (c *memConn) RemoteAddr() net.Addr             { return memAddr{} }
func (c *memConn) SetDeadline(time.Time) error      { return nil }
func (c *memConn) SetReadDeadline(time.Time) error  { return nil }
func (c *memConn) SetWriteDeadline(time.Time) error { return nil }

type memAddr struct{}

func (memAddr) Network() string { return "memory" }
func (memAddr) String() string  { return "memory" }
