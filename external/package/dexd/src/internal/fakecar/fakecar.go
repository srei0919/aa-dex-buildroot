// Package fakecar plays the car's side of an Android Auto session. It is a
// test double: used by the tests and by the aacar command-line tool, never by
// dexd itself. It encodes its own messages rather than reusing the phone's
// code, so a mistake in one is not mirrored in the other.
package fakecar

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"dexd/aaphone"
	"dexd/aatls"
	"dexd/aawire"
	"dexd/pb"
)

// Service ids the simulated car advertises.
const (
	InputService = 1
	VideoService = 3
	AudioService = 4
)

// Options shape the simulated car.
type Options struct {
	Video             []aaphone.VideoConfig // offered picture formats, in list order
	TouchW, TouchH    int
	NoVideo           bool // advertise no video sink
	NoTouch           bool // advertise no touchscreen
	MaxUnacked        int  // 0 means the field is left out
	NoAck             bool // never acknowledge pictures
	RejectTLS         bool // refuse the phone's certificate
	CloseAfterTLS     bool // hang up right after the handshake
	CloseAfterVersion bool // hang up right after the version exchange
	AllowedIndices    []int
	OnEvent           func(string) // optional trace
}

// Car is a running simulated car.
type Car struct {
	conn io.ReadWriteCloser
	o    Options
	eng  *aatls.Engine
	asm  aawire.Assembler
	wmu  sync.Mutex

	mu           sync.Mutex
	codecConfigs [][]byte
	frames       [][]byte
	started      bool
	sessionID    int
	configIndex  int
	byeReason    int
	byeSeen      bool
	pings        map[int64]chan struct{}
	err          error

	done      chan struct{}
	closeOnce sync.Once
}

// New starts the car's side of the session on conn.
func New(conn io.ReadWriteCloser, o Options) *Car {
	c := &Car{conn: conn, o: o, pings: map[int64]chan struct{}{}, done: make(chan struct{})}
	go func() { c.finish(c.run()) }()
	return c
}

func (c *Car) event(format string, args ...any) {
	if c.o.OnEvent != nil {
		c.o.OnEvent(fmt.Sprintf(format, args...))
	}
}

func (c *Car) finish(err error) {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		close(c.done)
		c.conn.Close()
	})
}

// Close hangs up.
func (c *Car) Close() { c.finish(errors.New("fakecar: closed")) }

// Done is closed when the car's side has ended.
func (c *Car) Done() <-chan struct{} { return c.done }

// Err says why the car's side ended.
func (c *Car) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func msg(id uint16, body []byte) []byte {
	return append(binary.BigEndian.AppendUint16(nil, id), body...)
}

func (c *Car) sendPlain(id uint16, body []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return aawire.WriteFrame(c.conn, aawire.Frame{Flags: aawire.FlagFirst | aawire.FlagLast, Payload: msg(id, body)})
}

func (c *Car) send(channel uint8, extra uint8, id uint16, body []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	for _, f := range aawire.Split(channel, aawire.FlagEncrypted|extra, msg(id, body)) {
		enc, err := c.eng.Encrypt(f.Payload)
		if err != nil {
			return err
		}
		f.Payload = enc
		if err := aawire.WriteFrame(c.conn, f); err != nil {
			return err
		}
	}
	return nil
}

// read returns the next whole message.
func (c *Car) read() (channel uint8, control bool, id uint16, body []byte, err error) {
	for {
		f, err := aawire.ReadFrame(c.conn)
		if err != nil {
			return 0, false, 0, nil, err
		}
		if f.Flags&aawire.FlagEncrypted != 0 {
			if f.Payload, err = c.eng.Decrypt(f.Payload); err != nil {
				return 0, false, 0, nil, err
			}
		}
		ch, m, done, err := c.asm.Add(f)
		if err != nil {
			return 0, false, 0, nil, err
		}
		if !done {
			continue
		}
		if len(m) < 2 {
			return 0, false, 0, nil, errors.New("fakecar: short message")
		}
		return ch, f.Flags&aawire.FlagControl != 0, binary.BigEndian.Uint16(m), m[2:], nil
	}
}

func (c *Car) run() error {
	// Version exchange: the car speaks first.
	if err := c.sendPlain(aaphone.MsgVersionRequest, []byte{0, 1, 0, 6}); err != nil {
		return err
	}
	if _, _, id, _, err := c.read(); err != nil || id != aaphone.MsgVersionResponse {
		return fmt.Errorf("fakecar: expected a version response (id %d): %v", id, err)
	}

	if c.o.CloseAfterVersion {
		return errors.New("fakecar: hung up after the version exchange")
	}
	cfg := &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS12}
	if c.o.RejectTLS {
		cfg = &tls.Config{RootCAs: x509.NewCertPool(), ServerName: "phone", MaxVersion: tls.VersionTLS12}
	}
	c.eng = aatls.NewClient(cfg)
	err := c.eng.Handshake(
		func(b []byte) error { return c.sendPlain(aaphone.MsgSSL, b) },
		func() ([]byte, error) {
			_, _, id, body, err := c.read()
			if err != nil {
				return nil, err
			}
			if id != aaphone.MsgSSL {
				return nil, fmt.Errorf("fakecar: message %d during the handshake", id)
			}
			return body, nil
		},
	)
	if err != nil {
		c.event("handshake failed: %v", err)
		return err
	}
	if c.o.CloseAfterTLS {
		return errors.New("fakecar: hung up after the handshake")
	}
	var auth pb.Writer
	auth.Varint(1, 0)
	if err := c.sendPlain(aaphone.MsgAuthComplete, auth.Out()); err != nil {
		return err
	}
	c.event("authenticated")

	for {
		ch, control, id, body, err := c.read()
		if err != nil {
			return err
		}
		if err := c.handle(ch, control, id, body); err != nil {
			return err
		}
	}
}

func (c *Car) handle(ch uint8, control bool, id uint16, body []byte) error {
	if control && ch != 0 {
		if id == aaphone.MsgChannelOpenRequest {
			c.event("channel %d opened", ch)
			var w pb.Writer
			w.Varint(1, 0)
			return c.send(ch, aawire.FlagControl, aaphone.MsgChannelOpenResponse, w.Out())
		}
		return nil
	}
	switch ch {
	case 0:
		return c.handleControl(id, body)
	case VideoService:
		return c.handleVideo(id, body)
	case InputService:
		if id == aaphone.MsgKeyBindingRequest {
			var w pb.Writer
			w.Varint(1, 0)
			return c.send(ch, 0, aaphone.MsgKeyBindingResponse, w.Out())
		}
	}
	return nil
}

func (c *Car) handleControl(id uint16, body []byte) error {
	switch id {
	case aaphone.MsgSDRequest:
		c.event("service discovery requested")
		return c.send(0, 0, aaphone.MsgSDResponse, c.services())
	case aaphone.MsgPingRequest:
		return c.send(0, 0, aaphone.MsgPingResponse, body)
	case aaphone.MsgPingResponse:
		ts := int64(firstVarint(body, 1))
		c.mu.Lock()
		if w, ok := c.pings[ts]; ok {
			close(w)
			delete(c.pings, ts)
		}
		c.mu.Unlock()
	case aaphone.MsgByeByeRequest:
		c.mu.Lock()
		c.byeReason, c.byeSeen = int(firstVarint(body, 1)), true
		c.mu.Unlock()
		c.event("phone said goodbye (reason %d)", firstVarint(body, 1))
		return c.send(0, 0, aaphone.MsgByeByeResponse, nil)
	case aaphone.MsgByeByeResponse:
		return errors.New("fakecar: goodbye acknowledged")
	}
	return nil
}

func (c *Car) handleVideo(id uint16, body []byte) error {
	switch id {
	case aaphone.MsgSetup:
		var w pb.Writer
		w.Varint(1, 2) // ready
		if c.o.MaxUnacked > 0 {
			w.Varint(2, uint64(c.o.MaxUnacked))
		}
		for _, i := range c.o.AllowedIndices {
			w.Varint(3, uint64(i))
		}
		if err := c.send(VideoService, 0, aaphone.MsgConfig, w.Out()); err != nil {
			return err
		}
		var focus pb.Writer
		focus.Varint(1, 1) // projected
		return c.send(VideoService, 0, aaphone.MsgVideoFocus, focus.Out())
	case aaphone.MsgStart:
		c.mu.Lock()
		c.started = true
		c.sessionID, c.configIndex = int(firstVarint(body, 1)), int(firstVarint(body, 2))
		c.mu.Unlock()
		c.event("video started (session %d, config %d)", c.sessionID, c.configIndex)
	case aaphone.MsgCodecData:
		c.mu.Lock()
		c.codecConfigs = append(c.codecConfigs, body)
		c.mu.Unlock()
		c.event("codec config, %d bytes", len(body))
		return c.ack()
	case aaphone.MsgData:
		if len(body) < 8 {
			return errors.New("fakecar: picture without a timestamp")
		}
		c.mu.Lock()
		c.frames = append(c.frames, body[8:])
		n := len(c.frames)
		c.mu.Unlock()
		c.event("picture %d, %d bytes", n, len(body)-8)
		return c.ack()
	}
	return nil
}

func (c *Car) ack() error {
	if c.o.NoAck {
		return nil
	}
	c.mu.Lock()
	session := c.sessionID
	c.mu.Unlock()
	var w pb.Writer
	w.Varint(1, uint64(session))
	w.Varint(2, 1)
	return c.send(VideoService, 0, aaphone.MsgAck, w.Out())
}

func (c *Car) services() []byte {
	var resp pb.Writer
	if !c.o.NoTouch {
		var screen, input, svc pb.Writer
		screen.Varint(1, uint64(c.o.TouchW))
		screen.Varint(2, uint64(c.o.TouchH))
		input.Bytes(2, screen.Out())
		svc.Varint(1, InputService)
		svc.Bytes(4, input.Out())
		resp.Bytes(1, svc.Out())
	}
	if !c.o.NoVideo {
		var sink, svc pb.Writer
		sink.Varint(1, aaphone.CodecH264)
		for _, v := range c.o.Video {
			var vc pb.Writer
			vc.Varint(1, resolutionCode(v))
			if v.FPS == 60 {
				vc.Varint(2, 1)
			} else {
				vc.Varint(2, 2)
			}
			if v.MarginW != 0 {
				vc.Varint(3, uint64(v.MarginW))
			}
			if v.MarginH != 0 {
				vc.Varint(4, uint64(v.MarginH))
			}
			sink.Bytes(4, vc.Out())
		}
		svc.Varint(1, VideoService)
		svc.Bytes(3, sink.Out())
		resp.Bytes(1, svc.Out())
	}
	var audio, svc pb.Writer
	audio.Varint(1, 1) // PCM audio sink, to check it is not mistaken for video
	svc.Varint(1, AudioService)
	svc.Bytes(3, audio.Out())
	resp.Bytes(1, svc.Out())
	resp.String(2, "Simulated")
	resp.String(3, "Car")
	return resp.Out()
}

func resolutionCode(v aaphone.VideoConfig) uint64 {
	switch {
	case v.Width == 800 && v.Height == 480:
		return 1
	case v.Width == 1280 && v.Height == 720:
		return 2
	case v.Width == 1920 && v.Height == 1080:
		return 3
	}
	return 4 // 2560x1440: a size dexd must skip
}

func firstVarint(b []byte, field int) uint64 {
	fs, _ := pb.Parse(b)
	for _, f := range fs {
		if f.Num == field && f.Wire == pb.WireVarint {
			return f.Val
		}
	}
	return 0
}

// Touch sends one touchscreen event with a single pointer.
func (c *Car) Touch(x, y, action int) {
	var p, ev, rep pb.Writer
	p.Varint(1, uint64(x))
	p.Varint(2, uint64(y))
	p.Varint(3, 0)
	ev.Bytes(1, p.Out())
	ev.Varint(2, 0)
	ev.Varint(3, uint64(action))
	rep.Varint(1, uint64(time.Now().UnixMicro()))
	rep.Bytes(3, ev.Out())
	c.send(InputService, 0, aaphone.MsgInputReport, rep.Out())
}

// Ping sends a ping and waits for the phone's echo.
func (c *Car) Ping(timestamp int64) (echo int64, err error) {
	w := make(chan struct{})
	c.mu.Lock()
	c.pings[timestamp] = w
	c.mu.Unlock()
	var req pb.Writer
	req.Varint(1, uint64(timestamp))
	if err := c.send(0, 0, aaphone.MsgPingRequest, req.Out()); err != nil {
		return 0, err
	}
	select {
	case <-w:
		return timestamp, nil
	case <-c.done:
		return 0, c.Err()
	case <-time.After(2 * time.Second):
		return 0, errors.New("fakecar: no ping reply")
	}
}

// Bye tells the phone the car is ending the session.
func (c *Car) Bye() {
	var w pb.Writer
	w.Varint(1, 1)
	c.send(0, 0, aaphone.MsgByeByeRequest, w.Out())
}

// ByeReason reports the reason of a goodbye received from the phone.
func (c *Car) ByeReason() (reason int, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.byeReason, c.byeSeen
}

// CodecConfigs returns every stream-parameter message received.
func (c *Car) CodecConfigs() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]byte(nil), c.codecConfigs...)
}

// Frames returns every picture received, timestamps removed.
func (c *Car) Frames() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]byte(nil), c.frames...)
}

// Started reports the stream start the phone sent, if any.
func (c *Car) Started() (sessionID, configIndex int, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID, c.configIndex, c.started
}
