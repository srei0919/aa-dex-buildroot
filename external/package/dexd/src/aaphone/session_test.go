package aaphone_test

import (
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"sync"
	"testing"
	"time"

	"dexd/aaphone"
	"dexd/aatls"
	"dexd/internal/fakecar"
)

var (
	certOnce sync.Once
	cert     tls.Certificate
)

func testCert(t testing.TB) tls.Certificate {
	t.Helper()
	certOnce.Do(func() {
		c, k, err := aatls.GeneratePEM()
		if err != nil {
			t.Fatal(err)
		}
		if cert, err = tls.X509KeyPair(c, k); err != nil {
			t.Fatal(err)
		}
	})
	return cert
}

func twoSizes() []aaphone.VideoConfig {
	return []aaphone.VideoConfig{
		{Index: 0, Width: 800, Height: 480, FPS: 30},
		{Index: 1, Width: 1280, Height: 720, FPS: 30, MarginH: 40},
	}
}

// link starts a simulated car on one end of a pipe and returns the phone's end.
func link(t *testing.T, o fakecar.Options) (net.Conn, *fakecar.Car) {
	t.Helper()
	phoneEnd, carEnd := net.Pipe()
	car := fakecar.New(carEnd, o)
	t.Cleanup(func() { car.Close(); phoneEnd.Close() })
	return phoneEnd, car
}

func start(t *testing.T, o fakecar.Options) (*aaphone.Session, *fakecar.Car) {
	t.Helper()
	conn, car := link(t, o)
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	s, err := aaphone.Start(conn, aaphone.Options{Cert: testCert(t), DeviceName: "CarDeX", Log: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	conn.SetDeadline(time.Time{})
	return s, car
}

func TestStartReachesDiscovery(t *testing.T) {
	s, _ := start(t, fakecar.Options{Video: twoSizes(), TouchW: 800, TouchH: 480})
	car := s.Car()
	if !car.HasVideo || len(car.Video) != 2 || car.Video[1] != twoSizes()[1] {
		t.Errorf("video: %+v", car)
	}
	if !car.HasTouch || car.TouchW != 800 || car.TouchH != 480 {
		t.Errorf("touch: %+v", car)
	}
}

func TestPingIsEchoed(t *testing.T) {
	_, car := start(t, fakecar.Options{Video: twoSizes(), TouchW: 800, TouchH: 480})
	echo, err := car.Ping(12345)
	if err != nil || echo != 12345 {
		t.Fatalf("echo %d err %v", echo, err)
	}
}

func stageOf(err error) string {
	var se *aaphone.StageError
	if errors.As(err, &se) {
		return se.Stage
	}
	return ""
}

func TestCarRejectsTLS(t *testing.T) {
	conn, _ := link(t, fakecar.Options{RejectTLS: true})
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	_, err := aaphone.Start(conn, aaphone.Options{Cert: testCert(t), DeviceName: "CarDeX"})
	if stageOf(err) != "tls" {
		t.Fatalf("want a tls stage error, got %v", err)
	}
}

func TestCarClosesBeforeAuth(t *testing.T) {
	conn, _ := link(t, fakecar.Options{CloseAfterTLS: true})
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	_, err := aaphone.Start(conn, aaphone.Options{Cert: testCert(t), DeviceName: "CarDeX"})
	if stageOf(err) != "auth" {
		t.Fatalf("want an auth stage error, got %v", err)
	}
}

func TestCarByeEndsSession(t *testing.T) {
	s, car := start(t, fakecar.Options{Video: twoSizes(), TouchW: 800, TouchH: 480})
	car.Bye()
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Fatal("session still open one second after the car said goodbye")
	}
	if s.Err() == nil {
		t.Error("Err() should say why the session ended")
	}
}

func TestCarHangsUpEndsSession(t *testing.T) {
	s, car := start(t, fakecar.Options{Video: twoSizes(), TouchW: 800, TouchH: 480})
	car.Close()
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Fatal("session still open one second after the car closed the link")
	}
}

func TestByeFromPhone(t *testing.T) {
	s, car := start(t, fakecar.Options{Video: twoSizes(), TouchW: 800, TouchH: 480})
	if err := s.Bye(); err != nil {
		t.Fatal(err)
	}
	if reason, ok := car.ByeReason(); !ok || reason != 1 {
		t.Fatalf("car saw reason %d ok %v", reason, ok)
	}
	select {
	case <-s.Done():
	default:
		t.Error("session should be closed after Bye")
	}
}

func TestRejectionMeansCarSawCertificate(t *testing.T) {
	conn, _ := link(t, fakecar.Options{RejectTLS: true})
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	_, err := aaphone.Start(conn, aaphone.Options{Cert: testCert(t), DeviceName: "CarDeX"})
	var se *aaphone.StageError
	if !errors.As(err, &se) || !se.CarSawCert {
		t.Fatalf("want CarSawCert, got %#v", err)
	}
}

func TestHangUpBeforeHandshakeIsNotRejection(t *testing.T) {
	conn, _ := link(t, fakecar.Options{CloseAfterVersion: true})
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	_, err := aaphone.Start(conn, aaphone.Options{Cert: testCert(t), DeviceName: "CarDeX"})
	var se *aaphone.StageError
	if !errors.As(err, &se) || se.CarSawCert {
		t.Fatalf("want a stage error without CarSawCert, got %#v", err)
	}
}
