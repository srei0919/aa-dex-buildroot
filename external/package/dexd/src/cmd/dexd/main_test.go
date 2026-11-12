package main

import (
	"image/png"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dexd/aaphone"
	"dexd/internal/fakecar"
	"dexd/logx"
)

// fastClock runs the menu's seconds 200 times faster.
type fastClock struct{}

func (fastClock) Now() time.Time                         { return time.Now() }
func (fastClock) After(d time.Duration) <-chan time.Time { return time.After(d / 200) }

type result struct {
	reason string
	car    *fakecar.Car
}

// runWithCar runs dexd against a simulated car and returns why dexd stopped.
func runWithCar(t *testing.T, data string, o fakecar.Options) result {
	t.Helper()
	if o.Video == nil && !o.NoVideo {
		o.Video = []aaphone.VideoConfig{{Index: 0, Width: 800, Height: 480, FPS: 30}}
		o.TouchW, o.TouchH = 800, 480
	}
	cars := make(chan *fakecar.Car, 1)
	lg := logx.Open(data)
	lg.Quiet()
	defer lg.Close()
	cfg := config{
		listen:        "127.0.0.1:0",
		certs:         t.TempDir(),
		data:          data,
		acceptTimeout: 5 * time.Second,
		startTimeout:  5 * time.Second,
		clock:         fastClock{},
		log:           lg,
		ready: func(addr string) {
			go func() {
				conn, err := net.Dial("tcp", addr)
				if err != nil {
					t.Error(err)
					return
				}
				cars <- fakecar.New(conn, o)
			}()
		},
	}
	done := make(chan string, 1)
	go func() { done <- run(cfg) }()
	select {
	case r := <-done:
		var car *fakecar.Car
		select {
		case car = <-cars:
		default:
		}
		return result{r, car}
	case <-time.After(20 * time.Second):
		t.Fatal("dexd did not stop")
		return result{}
	}
}

func TestMainMenuTimeoutEndToEnd(t *testing.T) {
	r := runWithCar(t, t.TempDir(), fakecar.Options{})
	if r.reason != "menu timeout" {
		t.Fatalf("reason %q", r.reason)
	}
	if r.car == nil || len(r.car.Frames()) < 1 || len(r.car.CodecConfigs()) != 1 {
		t.Fatalf("car received no pictures")
	}
}

func TestMainMarksRejectedSelfSigned(t *testing.T) {
	data := t.TempDir()
	r := runWithCar(t, data, fakecar.Options{RejectTLS: true})
	if _, err := os.Stat(filepath.Join(data, "selfsigned-rejected")); err != nil {
		t.Fatalf("no marker (reason %q)", r.reason)
	}
	if !strings.Contains(r.reason, "certificate") {
		t.Errorf("reason %q should mention the certificate", r.reason)
	}
}

func TestMainHangUpDoesNotMark(t *testing.T) {
	data := t.TempDir()
	runWithCar(t, data, fakecar.Options{CloseAfterVersion: true})
	if _, err := os.Stat(filepath.Join(data, "selfsigned-rejected")); err == nil {
		t.Fatal("a hang-up before the handshake must not count as a rejection")
	}
}

func TestMainClearsMarkerOnSuccess(t *testing.T) {
	data := t.TempDir()
	os.WriteFile(filepath.Join(data, "selfsigned-rejected"), nil, 0o644)
	runWithCar(t, data, fakecar.Options{})
	if _, err := os.Stat(filepath.Join(data, "selfsigned-rejected")); err == nil {
		t.Fatal("marker should be removed after a successful session")
	}
}

func TestMainNothingUsable(t *testing.T) {
	r := runWithCar(t, t.TempDir(), fakecar.Options{NoVideo: true, TouchW: 800, TouchH: 480})
	if !strings.HasPrefix(r.reason, "car offers nothing usable") {
		t.Fatalf("reason %q", r.reason)
	}
}

func TestMainNoConnection(t *testing.T) {
	lg := logx.Open(t.TempDir())
	lg.Quiet()
	defer lg.Close()
	reason := run(config{listen: "127.0.0.1:0", certs: t.TempDir(), data: t.TempDir(),
		acceptTimeout: 100 * time.Millisecond, startTimeout: time.Second, clock: fastClock{}, log: lg})
	if !strings.Contains(reason, "no connection") {
		t.Fatalf("reason %q", reason)
	}
}

func TestRenderWritesPNGs(t *testing.T) {
	dir := t.TempDir()
	if err := render(dir); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.png"))
	if len(files) != 6 {
		t.Fatalf("%d files", len(files))
	}
	for _, f := range files {
		r, _ := os.Open(f)
		if _, err := png.Decode(r); err != nil {
			t.Errorf("%s: %v", f, err)
		}
		r.Close()
	}
}

func TestStatusPage(t *testing.T) {
	lg := logx.Open(t.TempDir())
	lg.Quiet()
	defer lg.Close()
	lg.Printf("hello from the log")
	st := &status{}
	st.set(func(s *status) {
		s.State, s.Make, s.Model = "menu", "Genesis", "G80"
		s.Offered = []aaphone.VideoConfig{{Width: 800, Height: 480, FPS: 30}}
		s.Chosen = aaphone.VideoConfig{Width: 800, Height: 480, FPS: 30}
	})
	rec := httptest.NewRecorder()
	statusHandler(st, lg).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	body, _ := io.ReadAll(rec.Body)
	for _, want := range []string{"state: menu", "Genesis", "800x480", "conversion: off", "hello from the log"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("page lacks %q:\n%s", want, body)
		}
	}
}

func TestDefaultListenIsNotAaProxyPort(t *testing.T) {
	// aa-proxy-rs itself listens on 0.0.0.0:5288 and 5277.
	if defaultListen != "127.0.0.1:5290" {
		t.Fatalf("defaultListen %q", defaultListen)
	}
}

func TestMainStallAfterCertDoesNotMark(t *testing.T) {
	data := t.TempDir()
	lg := logx.Open(data)
	lg.Quiet()
	defer lg.Close()
	cfg := config{listen: "127.0.0.1:0", certs: t.TempDir(), data: data,
		acceptTimeout: 5 * time.Second, startTimeout: 300 * time.Millisecond, clock: fastClock{}, log: lg,
		ready: func(addr string) {
			go func() {
				if conn, err := net.Dial("tcp", addr); err == nil {
					fakecar.New(conn, fakecar.Options{StallAfterTLS: true})
				}
			}()
		}}
	reason := run(cfg)
	if _, err := os.Stat(filepath.Join(data, "selfsigned-rejected")); err == nil {
		t.Fatalf("a car that went quiet after the handshake is not a rejection (reason %q)", reason)
	}
}
