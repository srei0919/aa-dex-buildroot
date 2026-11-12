package aaphone

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"dexd/aatls"
	"dexd/aawire"
)

// Options configure a session.
type Options struct {
	Cert       tls.Certificate
	DeviceName string
	Log        *log.Logger // nil discards
	// WriteTimeout bounds each write to the car; 0 means 5 s.
	WriteTimeout time.Duration
}

// StageError says how far session start got before failing.
// Stage is "version", "tls", "auth" or "discovery".
//
// CarSawCert is true when the car had already received the phone's
// certificate when the session failed: the TLS stage after the phone's first
// handshake reply, or the authentication stage. Only then can the failure be
// the car rejecting the certificate.
type StageError struct {
	Stage      string
	Err        error
	CarSawCert bool
}

func (e *StageError) Error() string { return "aaphone: " + e.Stage + ": " + e.Err.Error() }
func (e *StageError) Unwrap() error { return e.Err }

// ErrCarBye is the session error after the car said goodbye.
var ErrCarBye = errors.New("aaphone: the car ended the session")

// Session is one Android Auto session, seen from the phone.
type Session struct {
	conn io.ReadWriteCloser
	eng  *aatls.Engine
	log  *log.Logger
	car  Car

	wmu sync.Mutex // one message on the wire at a time
	asm aawire.Assembler

	done      chan struct{}
	closeOnce sync.Once
	errMu     sync.Mutex
	err       error

	byeAck     chan struct{}
	byeAckOnce sync.Once

	waitMu  sync.Mutex
	waiters map[waitKey]chan []byte

	video videoState
	touch chan Touch

	writeTimeout time.Duration
}

type waitKey struct {
	channel uint8
	control bool
	id      uint16
}

// Start runs the session from the car's first message through its service
// list. conn carries the frames; deadlines, if wanted, are the caller's.
func Start(conn io.ReadWriteCloser, o Options) (*Session, error) {
	logger := o.Log
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	s := &Session{
		conn:    conn,
		eng:     aatls.NewServer(o.Cert),
		log:     logger,
		done:    make(chan struct{}),
		byeAck:  make(chan struct{}),
		waiters: map[waitKey]chan []byte{},
		touch:   make(chan Touch, 64),

		writeTimeout: o.WriteTimeout,
	}
	if s.writeTimeout <= 0 {
		s.writeTimeout = 5 * time.Second
	}
	s.video.window = 1
	s.video.configs = make(chan []byte, 8)
	s.video.projected = true
	sentCert := false
	fail := func(stage string, err error) (*Session, error) {
		conn.Close()
		saw := stage == "auth" || (stage == "tls" && sentCert)
		return nil, &StageError{Stage: stage, Err: err, CarSawCert: saw}
	}

	// 1. The car announces its protocol version; answer with the same one.
	_, _, id, body, err := s.readMessage()
	if err != nil {
		return fail("version", err)
	}
	if id != MsgVersionRequest || len(body) < 4 {
		return fail("version", fmt.Errorf("expected a version request, got message %d with %d bytes", id, len(body)))
	}
	s.log.Printf("car protocol version %d.%d", int(body[0])<<8|int(body[1]), int(body[2])<<8|int(body[3]))
	if err := s.sendPlain(MsgVersionResponse, []byte{body[0], body[1], body[2], body[3], 0, 0}); err != nil {
		return fail("version", err)
	}

	// 2. TLS handshake, carried in control messages. The car is the client.
	err = s.eng.Handshake(
		func(b []byte) error {
			sentCert = true // the server's first flight carries its certificate
			return s.sendPlain(MsgSSL, b)
		},
		func() ([]byte, error) {
			_, _, id, body, err := s.readMessage()
			if err != nil {
				return nil, err
			}
			if id != MsgSSL {
				return nil, fmt.Errorf("message %d during the handshake", id)
			}
			return body, nil
		},
	)
	if err != nil {
		return fail("tls", err)
	}

	// 3. The car confirms authentication.
	_, _, id, body, err = s.readMessage()
	if err != nil {
		return fail("auth", err)
	}
	if id != MsgAuthComplete {
		return fail("auth", fmt.Errorf("expected authentication result, got message %d", id))
	}
	if status, err := firstVarint(body, 1, 0); err != nil || status != 0 {
		return fail("auth", fmt.Errorf("car reported authentication status %d (%v)", status, err))
	}

	// 4. Ask what the car offers.
	if err := s.send(0, 0, MsgSDRequest, buildServiceDiscoveryRequest(o.DeviceName)); err != nil {
		return fail("discovery", err)
	}
	for {
		ch, _, id, body, err := s.readMessage()
		if err != nil {
			return fail("discovery", err)
		}
		if ch == 0 && id == MsgPingRequest {
			if err := s.send(0, 0, MsgPingResponse, body); err != nil {
				return fail("discovery", err)
			}
			continue
		}
		if ch != 0 || id != MsgSDResponse {
			s.log.Printf("ignored message %#x on channel %d while waiting for the service list", id, ch)
			continue
		}
		// The whole reply, so a car that insists on services dexd does not
		// open can be diagnosed from one log.
		s.log.Printf("service discovery reply (%d bytes): %x", len(body), body)
		if s.car, err = parseServiceDiscoveryResponse(body); err != nil {
			return fail("discovery", err)
		}
		break
	}
	s.log.Printf("car %q %q: video=%v %+v, touch=%v %dx%d", s.car.Make, s.car.Model,
		s.car.HasVideo, s.car.Video, s.car.HasTouch, s.car.TouchW, s.car.TouchH)

	go s.readLoop()
	return s, nil
}

// Car returns what the car offered.
func (s *Session) Car() Car { return s.car }

// Done is closed when the session has ended for any reason.
func (s *Session) Done() <-chan struct{} { return s.done }

// Err says why the session ended; nil while it is running or after Bye.
func (s *Session) Err() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.err
}

func (s *Session) end(err error) {
	s.closeOnce.Do(func() {
		s.errMu.Lock()
		s.err = err
		s.errMu.Unlock()
		close(s.done)
		s.conn.Close()
	})
}

// Bye tells the car the phone is leaving, waits up to a second for the
// car's reply, and closes the session.
func (s *Session) Bye() error {
	err := s.send(0, 0, MsgByeByeRequest, buildByeBye(1))
	if err == nil {
		select {
		case <-s.byeAck:
		case <-s.done:
		case <-time.After(time.Second):
		}
	}
	s.end(nil)
	return err
}

// sendPlain writes an unencrypted control message (before authentication).
func (s *Session) sendPlain(id uint16, body []byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.armWrite()
	return aawire.WriteFrame(s.conn, aawire.Frame{Flags: aawire.FlagFirst | aawire.FlagLast, Payload: message(id, body)})
}

// armWrite bounds the next write, so a car (or aa-proxy-rs) that stops
// reading cannot block dexd forever. Caller holds wmu.
func (s *Session) armWrite() {
	if d, ok := s.conn.(interface{ SetWriteDeadline(time.Time) error }); ok {
		d.SetWriteDeadline(time.Now().Add(s.writeTimeout))
	}
}

// send writes an encrypted message, fragmenting it as needed.
func (s *Session) send(channel, extraFlags uint8, id uint16, body []byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	for _, f := range aawire.Split(channel, aawire.FlagEncrypted|extraFlags, message(id, body)) {
		enc, err := s.eng.Encrypt(f.Payload)
		if err != nil {
			return err
		}
		f.Payload = enc
		s.armWrite()
		if err := aawire.WriteFrame(s.conn, f); err != nil {
			// A frame may be half written: the session cannot continue.
			go s.end(fmt.Errorf("writing to the car: %w", err))
			return err
		}
	}
	return nil
}

// readMessage returns the next whole message from the car.
func (s *Session) readMessage() (channel uint8, control bool, id uint16, body []byte, err error) {
	for {
		f, err := aawire.ReadFrame(s.conn)
		if err != nil {
			return 0, false, 0, nil, err
		}
		if f.Flags&aawire.FlagEncrypted != 0 {
			if f.Payload, err = s.eng.Decrypt(f.Payload); err != nil {
				return 0, false, 0, nil, err
			}
		}
		ch, m, done, err := s.asm.Add(f)
		if err != nil {
			return 0, false, 0, nil, err
		}
		if !done {
			continue
		}
		id, body, err := splitMessage(m)
		if err != nil {
			return 0, false, 0, nil, err
		}
		return ch, f.Flags&aawire.FlagControl != 0, id, body, nil
	}
}

func (s *Session) readLoop() {
	for {
		ch, control, id, body, err := s.readMessage()
		if err != nil {
			s.end(err)
			return
		}
		if err := s.handle(ch, control, id, body); err != nil {
			s.end(err)
			return
		}
	}
}

func (s *Session) handle(ch uint8, control bool, id uint16, body []byte) error {
	if w := s.takeWaiter(waitKey{ch, control, id}); w != nil {
		w <- body
		return nil
	}
	if ch == 0 {
		switch id {
		case MsgPingRequest:
			// Replies never block this goroutine: it must keep reading, or a
			// car waiting for us to read could stall a write in progress.
			go s.send(0, 0, MsgPingResponse, body)
		case MsgByeByeRequest:
			s.log.Printf("car said goodbye")
			go s.send(0, 0, MsgByeByeResponse, nil)
			return ErrCarBye
		case MsgByeByeResponse:
			s.byeAckOnce.Do(func() { close(s.byeAck) })
		default:
			s.log.Printf("ignored control message %d (%d bytes)", id, len(body))
		}
		return nil
	}
	if !control && s.handleChannel(ch, id, body) {
		return nil
	}
	s.log.Printf("ignored message %#x on channel %d (%d bytes)", id, ch, len(body))
	return nil
}

// expect registers interest in one reply and returns where it will arrive.
func (s *Session) expect(k waitKey) <-chan []byte {
	c := make(chan []byte, 1)
	s.waitMu.Lock()
	s.waiters[k] = c
	s.waitMu.Unlock()
	return c
}

func (s *Session) takeWaiter(k waitKey) chan []byte {
	s.waitMu.Lock()
	defer s.waitMu.Unlock()
	c := s.waiters[k]
	delete(s.waiters, k)
	return c
}

// await waits for a registered reply, the end of the session, or a timeout.
func (s *Session) await(c <-chan []byte, what string, d time.Duration) ([]byte, error) {
	select {
	case b := <-c:
		return b, nil
	case <-s.done:
		if err := s.Err(); err != nil {
			return nil, fmt.Errorf("waiting for %s: %w", what, err)
		}
		return nil, fmt.Errorf("waiting for %s: session closed", what)
	case <-time.After(d):
		return nil, fmt.Errorf("no %s from the car within %v", what, d)
	}
}
