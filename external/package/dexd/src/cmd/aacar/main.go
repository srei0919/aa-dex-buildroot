// aacar plays a car against a running dexd, for testing without a car.
// It saves what dexd sends as a raw H.264 file that VLC can play.
//
//	aacar -connect 127.0.0.1:5288 -sizes 480,720 -out menu.h264 -touch "3,960,360"
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"dexd/aaphone"
	"dexd/internal/fakecar"
)

func main() {
	addr := flag.String("connect", "127.0.0.1:5288", "dexd address")
	out := flag.String("out", "aacar.h264", "file for the received pictures")
	sizes := flag.String("sizes", "480,720", "offered heights: 480, 720, 1080")
	touch := flag.String("touch", "", "taps as seconds,x,y separated by ';' (touchscreen coordinates = picture size)")
	reject := flag.Bool("reject-tls", false, "refuse dexd's certificate")
	timeout := flag.Duration("timeout", 40*time.Second, "give up after this long")
	flag.Parse()

	o := fakecar.Options{RejectTLS: *reject, MaxUnacked: 2, OnEvent: func(s string) { fmt.Println(time.Now().Format("15:04:05.000"), s) }}
	for i, h := range strings.Split(*sizes, ",") {
		switch strings.TrimSpace(h) {
		case "480":
			o.Video = append(o.Video, aaphone.VideoConfig{Index: i, Width: 800, Height: 480, FPS: 30})
		case "720":
			o.Video = append(o.Video, aaphone.VideoConfig{Index: i, Width: 1280, Height: 720, FPS: 30})
		case "1080":
			o.Video = append(o.Video, aaphone.VideoConfig{Index: i, Width: 1920, Height: 1080, FPS: 30})
		default:
			fail("unknown size %q", h)
		}
	}
	// The touchscreen matches the largest offered size.
	for _, v := range o.Video {
		if v.Width > o.TouchW {
			o.TouchW, o.TouchH = v.Width, v.Height
		}
	}

	conn, err := net.Dial("tcp", *addr)
	if err != nil {
		fail("connect: %v", err)
	}
	car := fakecar.New(conn, o)

	for _, tap := range strings.Split(*touch, ";") {
		if strings.TrimSpace(tap) == "" {
			continue
		}
		f := strings.Split(tap, ",")
		if len(f) != 3 {
			fail("bad tap %q", tap)
		}
		at, _ := strconv.ParseFloat(f[0], 64)
		x, _ := strconv.Atoi(f[1])
		y, _ := strconv.Atoi(f[2])
		go func() {
			time.Sleep(time.Duration(at * float64(time.Second)))
			fmt.Printf("tap at %d,%d\n", x, y)
			car.Touch(x, y, 0)
			car.Touch(x, y, 1)
		}()
	}

	select {
	case <-car.Done():
		fmt.Println("session ended:", car.Err())
	case <-time.After(*timeout):
		fmt.Println("timeout")
		car.Close()
	}
	var data []byte
	for _, c := range car.CodecConfigs() {
		data = append(data, c...)
	}
	frames := car.Frames()
	for _, f := range frames {
		data = append(data, f...)
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		fail("write: %v", err)
	}
	fmt.Printf("saved %d pictures to %s\n", len(frames), *out)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "aacar: "+format+"\n", args...)
	os.Exit(1)
}
