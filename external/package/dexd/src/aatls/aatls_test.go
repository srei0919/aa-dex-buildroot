package aatls

import (
	"bytes"
	"crypto/tls"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

var (
	testCertOnce sync.Once
	testCert     tls.Certificate
)

// sharedCert generates one certificate for the whole test binary.
func sharedCert(t testing.TB) tls.Certificate {
	t.Helper()
	testCertOnce.Do(func() {
		certPEM, keyPEM, err := GeneratePEM()
		if err != nil {
			t.Fatal(err)
		}
		if testCert, err = tls.X509KeyPair(certPEM, keyPEM); err != nil {
			t.Fatal(err)
		}
	})
	return testCert
}

// chanConn is the car's end of the test link: each Write becomes one chunk.
type chanConn struct {
	out chan<- []byte
	in  <-chan []byte
	buf []byte
}

func (c *chanConn) Write(p []byte) (int, error) {
	c.out <- append([]byte(nil), p...)
	return len(p), nil
}
func (c *chanConn) Read(p []byte) (int, error) {
	if len(c.buf) == 0 {
		b, ok := <-c.in
		if !ok {
			return 0, io.EOF
		}
		c.buf = b
	}
	n := copy(p, c.buf)
	c.buf = c.buf[n:]
	return n, nil
}
func (c *chanConn) Close() error                     { return nil }
func (c *chanConn) LocalAddr() net.Addr              { return nil }
func (c *chanConn) RemoteAddr() net.Addr             { return nil }
func (c *chanConn) SetDeadline(time.Time) error      { return nil }
func (c *chanConn) SetReadDeadline(time.Time) error  { return nil }
func (c *chanConn) SetWriteDeadline(time.Time) error { return nil }

// handshakePair completes a handshake between a secure (the phone) and a TLS
// client (the car) and returns both ends plus the two chunk channels.
func handshakePair(t *testing.T, clientCfg *tls.Config) (*Engine, *tls.Conn, chan []byte, chan []byte) {
	t.Helper()
	toPhone, toCar := make(chan []byte, 64), make(chan []byte, 64)
	client := tls.Client(&chanConn{out: toPhone, in: toCar}, clientCfg)
	clientErr := make(chan error, 1)
	go func() { clientErr <- client.Handshake() }()
	s := NewServer(sharedCert(t))
	err := s.Handshake(
		func(b []byte) error { toCar <- b; return nil },
		func() ([]byte, error) { return <-toPhone, nil },
	)
	if err != nil {
		t.Fatalf("phone handshake: %v", err)
	}
	if err := <-clientErr; err != nil {
		t.Fatalf("car handshake: %v", err)
	}
	return s, client, toPhone, toCar
}

func carConfig() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS12}
}

func TestSecureRoundTrip(t *testing.T) {
	s, client, toPhone, toCar := handshakePair(t, carConfig())
	for _, size := range []int{20000, 1} {
		plain := bytes.Repeat([]byte{byte(size)}, size)

		enc, err := s.Encrypt(plain)
		if err != nil {
			t.Fatal(err)
		}
		toCar <- enc
		got := make([]byte, size)
		if _, err := io.ReadFull(client, got); err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("phone to car, %d bytes: %v", size, err)
		}

		go client.Write(plain)
		var back []byte
		for len(back) < size {
			dec, err := s.Decrypt(<-toPhone)
			if err != nil {
				t.Fatal(err)
			}
			back = append(back, dec...)
		}
		if !bytes.Equal(back, plain) {
			t.Fatalf("car to phone, %d bytes differ", size)
		}
	}
}

func TestSecureRSAOnlyClient(t *testing.T) {
	cfg := carConfig()
	cfg.CipherSuites = []uint16{tls.TLS_RSA_WITH_AES_128_CBC_SHA}
	_, client, _, _ := handshakePair(t, cfg)
	if cs := client.ConnectionState().CipherSuite; cs != tls.TLS_RSA_WITH_AES_128_CBC_SHA {
		t.Fatalf("negotiated %#x", cs)
	}
}

func TestDecryptTwoRecords(t *testing.T) {
	s, client, toPhone, _ := handshakePair(t, carConfig())
	go func() {
		client.Write([]byte("first"))
		client.Write([]byte("second"))
	}()
	both := append(<-toPhone, <-toPhone...)
	got, err := s.Decrypt(both)
	if err != nil || string(got) != "firstsecond" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestDecryptGarbageIsAnError(t *testing.T) {
	s, _, _, _ := handshakePair(t, carConfig())
	if _, err := s.Decrypt([]byte{23, 3, 3, 0, 4, 1, 2, 3, 4}); err == nil {
		t.Fatal("want error for a forged record")
	}
}

func TestHandshakeFailsWhenCarHangsUp(t *testing.T) {
	s := NewServer(sharedCert(t))
	err := s.Handshake(
		func([]byte) error { return nil },
		func() ([]byte, error) { return nil, io.EOF },
	)
	if err == nil {
		t.Fatal("want error")
	}
}

func TestClientEngineAgainstServerEngine(t *testing.T) {
	toPhone, toCar := make(chan []byte, 64), make(chan []byte, 64)
	car := NewClient(carConfig())
	carErr := make(chan error, 1)
	go func() {
		carErr <- car.Handshake(
			func(b []byte) error { toPhone <- b; return nil },
			func() ([]byte, error) { return <-toCar, nil },
		)
	}()
	phone := NewServer(sharedCert(t))
	if err := phone.Handshake(
		func(b []byte) error { toCar <- b; return nil },
		func() ([]byte, error) { return <-toPhone, nil },
	); err != nil {
		t.Fatalf("phone: %v", err)
	}
	if err := <-carErr; err != nil {
		t.Fatalf("car: %v", err)
	}
	enc, err := car.Encrypt([]byte("touch"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := phone.Decrypt(enc); err != nil || string(got) != "touch" {
		t.Fatalf("got %q err %v", got, err)
	}
	enc, err = phone.Encrypt([]byte("picture"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := car.Decrypt(enc); err != nil || string(got) != "picture" {
		t.Fatalf("got %q err %v", got, err)
	}
}
