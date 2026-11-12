// dexd pretends to be an Android Auto phone toward the car (through
// aa-proxy-rs) and shows a menu on the car screen. Whatever happens it exits
// with status 0; the supervisor script then restores normal Android Auto.
package main

import (
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dexd/aaphone"
	"dexd/logx"
	"dexd/mode"
	"dexd/screen"
)

const (
	deviceName    = "CarDeX"
	defaultListen = "127.0.0.1:5290" // aa-proxy-rs itself listens on 5288 and 5277
)

type config struct {
	listen, certs, data, http string
	acceptTimeout             time.Duration // no connection from aa-proxy-rs
	startTimeout              time.Duration // car session not established
	clock                     mode.Clock
	log                       *logx.Logger
	ready                     func(addr string) // tests: called once listening
}

func main() {
	cfg := config{acceptTimeout: 60 * time.Second, startTimeout: 20 * time.Second, clock: mode.RealClock{}}
	renderDir := flag.String("render", "", "write the screens as PNG files into this directory and exit")
	flag.StringVar(&cfg.listen, "listen", defaultListen, "address aa-proxy-rs connects to")
	flag.StringVar(&cfg.certs, "certs", "/etc/aa-proxy-rs", "directory with md_cert.pem and md_key.pem")
	flag.StringVar(&cfg.data, "data", "/data/dexd", "directory for the log, the self-generated certificate and markers")
	flag.StringVar(&cfg.http, "http", ":8081", "status page address (empty = none)")
	flag.Parse()

	if *renderDir != "" {
		if err := render(*renderDir); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		return
	}
	cfg.log = logx.Open(cfg.data)
	defer cfg.log.Close()
	reason := run(cfg)
	cfg.log.Printf("dexd exits: %s", reason)
}

// run does one session and returns why it ended.
func run(cfg config) string {
	lg := cfg.log
	st := &status{State: "starting"}
	if cfg.http != "" {
		go func() {
			if err := http.ListenAndServe(cfg.http, statusHandler(st, lg)); err != nil {
				lg.Printf("status page: %v", err)
			}
		}()
	}

	marker := filepath.Join(cfg.data, "selfsigned-rejected")
	cert, selfSigned, err := aaphone.LoadCert(cfg.certs, cfg.data)
	if err != nil {
		return "no certificate: " + err.Error()
	}
	if selfSigned {
		lg.Printf("using the self-generated certificate")
	} else {
		lg.Printf("using the supplied certificate")
	}

	ln, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		return "cannot listen: " + err.Error()
	}
	defer ln.Close()
	lg.Printf("waiting for aa-proxy-rs on %s", ln.Addr())
	if cfg.ready != nil {
		cfg.ready(ln.Addr().String())
	}
	if tl, ok := ln.(*net.TCPListener); ok {
		tl.SetDeadline(time.Now().Add(cfg.acceptTimeout))
	}
	conn, err := ln.Accept()
	if err != nil {
		return fmt.Sprintf("no connection from aa-proxy-rs within %v", cfg.acceptTimeout)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(cfg.startTimeout))
	session, err := aaphone.Start(conn, aaphone.Options{Cert: cert, DeviceName: deviceName, Log: lg.Std()})
	if err != nil {
		lg.Printf("session start failed: %v", err)
		var se *aaphone.StageError
		var ne net.Error
		timedOut := errors.Is(err, os.ErrDeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
		// Only a car that saw the certificate and then refused or hung up
		// counts as a rejection; a car that merely went quiet does not.
		if selfSigned && errors.As(err, &se) && se.CarSawCert && !timedOut {
			if f, ferr := os.Create(marker); ferr == nil {
				f.Sync()
				f.Close()
				if d, derr := os.Open(cfg.data); derr == nil {
					d.Sync()
					d.Close()
				}
			}
			return "car rejected the self-generated certificate (" + err.Error() + ")"
		}
		return "car session not established: " + err.Error()
	}
	conn.SetDeadline(time.Time{})
	os.Remove(marker)

	car := session.Car()
	lg.Printf("car: make %q model %q, offered video %v, touchscreen %dx%d", car.Make, car.Model, car.Video, car.TouchW, car.TouchH)
	st.set(func(s *status) { s.Make, s.Model, s.Offered = car.Make, car.Model, car.Video })

	return mode.Run(session, cfg.clock, func(s mode.Status) {
		lg.Printf("state %s %s (video %dx%d@%d)", s.State, s.Reason, s.Video.Width, s.Video.Height, s.Video.FPS)
		st.set(func(x *status) { x.State, x.Chosen = s.State, s.Video })
	})
}

// status is what the status page shows.
type status struct {
	mu          sync.Mutex
	State       string
	Make, Model string
	Offered     []aaphone.VideoConfig
	Chosen      aaphone.VideoConfig
}

func (s *status) set(f func(*status)) {
	s.mu.Lock()
	f(s)
	s.mu.Unlock()
}

func size(v aaphone.VideoConfig) string {
	return fmt.Sprintf("%dx%d@%d", v.Width, v.Height, v.FPS)
}

func statusHandler(st *status, lg *logx.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		var offered []string
		for _, v := range st.Offered {
			offered = append(offered, size(v))
		}
		chosen := "none"
		if st.Chosen.Width > 0 {
			chosen = size(st.Chosen)
		}
		head := fmt.Sprintf("state: %s\ncar: %s %s\noffered video: %s\nchosen video: %s\nconversion: off\n\n",
			st.State, st.Make, st.Model, strings.Join(offered, ", "), chosen)
		st.mu.Unlock()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, head)
		for _, l := range lg.Tail(200) {
			fmt.Fprintln(w, l)
		}
	})
}

// render writes the menu and placeholder screens for the three car sizes.
func render(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, sz := range [][2]int{{800, 480}, {1280, 720}, {1920, 1080}} {
		vis := screen.Visible(screen.VideoSize{W: sz[0], H: sz[1]})
		shots := map[string]screen.Screen{
			"menu":        screen.Menu(sz[0], sz[1], vis, 20),
			"placeholder": screen.Message(sz[0], sz[1], vis, []string{"DeX is not in this version yet.", "It arrives with the next update."}, true),
		}
		for name, s := range shots {
			if err := savePNG(filepath.Join(dir, fmt.Sprintf("%s-%dx%d.png", name, sz[0], sz[1])), s.Canvas); err != nil {
				return err
			}
		}
	}
	return nil
}

func savePNG(path string, c *screen.Canvas) error {
	img := image.NewRGBA(image.Rect(0, 0, c.W, c.H))
	for i := 0; i < c.W*c.H; i++ {
		img.Set(i%c.W, i/c.W, color.RGBA{c.Pix[3*i], c.Pix[3*i+1], c.Pix[3*i+2], 255})
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
