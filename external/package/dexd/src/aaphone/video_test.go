package aaphone_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"dexd/aaphone"
	"dexd/internal/fakecar"
)

func TestChooseVideo(t *testing.T) {
	v := func(i, w, h, fps int) aaphone.VideoConfig {
		return aaphone.VideoConfig{Index: i, Width: w, Height: h, FPS: fps}
	}
	cases := []struct {
		name    string
		cfgs    []aaphone.VideoConfig
		allowed []int
		want    int // index, -1 for none
	}{
		{"largest of two", []aaphone.VideoConfig{v(0, 800, 480, 30), v(1, 1280, 720, 30)}, nil, 1},
		{"1080 when offered", []aaphone.VideoConfig{v(0, 800, 480, 30), v(1, 1280, 720, 30), v(2, 1920, 1080, 30)}, nil, 2},
		{"30 preferred over 60", []aaphone.VideoConfig{v(0, 1280, 720, 60), v(1, 1280, 720, 30)}, nil, 1},
		{"60 when that is all", []aaphone.VideoConfig{v(0, 1280, 720, 60)}, nil, 0},
		{"size beats frame rate", []aaphone.VideoConfig{v(0, 800, 480, 30), v(1, 1280, 720, 60)}, nil, 1},
		{"nothing offered", nil, nil, -1},
		{"restricted by the car", []aaphone.VideoConfig{v(0, 800, 480, 30), v(1, 1280, 720, 30)}, []int{0}, 0},
		{"restriction names nothing usable", []aaphone.VideoConfig{v(0, 800, 480, 30)}, []int{5}, -1},
	}
	for _, c := range cases {
		got, ok := aaphone.ChooseVideo(c.cfgs, c.allowed)
		if (c.want == -1) != !ok || (ok && got.Index != c.want) {
			t.Errorf("%s: got index %d ok %v, want %d", c.name, got.Index, ok, c.want)
		}
	}
}

func openBoth(t *testing.T, o fakecar.Options) (*aaphone.Session, *fakecar.Car, aaphone.VideoConfig) {
	t.Helper()
	if o.Video == nil {
		o.Video = twoSizes()
	}
	o.TouchW, o.TouchH = 800, 480
	s, car := start(t, o)
	vc, err := s.OpenVideo()
	if err != nil {
		t.Fatalf("OpenVideo: %v", err)
	}
	if err := s.OpenInput(); err != nil {
		t.Fatalf("OpenInput: %v", err)
	}
	return s, car, vc
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestOpenVideoStarts(t *testing.T) {
	_, car, vc := openBoth(t, fakecar.Options{MaxUnacked: 2})
	if vc.Width != 1280 || vc.Height != 720 || vc.MarginH != 40 {
		t.Errorf("chose %+v", vc)
	}
	eventually(t, "stream start", func() bool { _, _, ok := car.Started(); return ok })
	if session, index, _ := car.Started(); session != 1 || index != 1 {
		t.Errorf("start: session %d index %d", session, index)
	}
}

func TestOpenVideoHonoursAllowedIndices(t *testing.T) {
	_, _, vc := openBoth(t, fakecar.Options{AllowedIndices: []int{0}})
	if vc.Width != 800 {
		t.Errorf("chose %+v, want the 800x480 entry", vc)
	}
}

func TestCodecConfigAndFrames(t *testing.T) {
	s, car, _ := openBoth(t, fakecar.Options{MaxUnacked: 8})
	cfg := []byte{0, 0, 0, 1, 0x67, 1, 2, 3, 0, 0, 0, 1, 0x68, 4}
	if err := s.SendCodecConfig(cfg); err != nil {
		t.Fatal(err)
	}
	pics := [][]byte{bytes.Repeat([]byte{1}, 10), bytes.Repeat([]byte{2}, 40000), bytes.Repeat([]byte{3}, 5)}
	for i, p := range pics {
		if sent, err := s.SendFrame(p); err != nil || !sent {
			t.Fatalf("picture %d: sent %v err %v", i, sent, err)
		}
	}
	eventually(t, "three pictures", func() bool { return len(car.Frames()) == 3 })
	if got := car.CodecConfigs(); len(got) != 1 || !bytes.Equal(got[0], cfg) {
		t.Errorf("codec config: %x", got)
	}
	for i, got := range car.Frames() {
		if !bytes.Equal(got, pics[i]) {
			t.Errorf("picture %d differs (len %d)", i, len(got))
		}
	}
}

func TestWindowFullDoesNotBlock(t *testing.T) {
	s, _, _ := openBoth(t, fakecar.Options{MaxUnacked: 2, NoAck: true})
	for i := 0; i < 2; i++ {
		if sent, err := s.SendFrame([]byte{byte(i)}); err != nil || !sent {
			t.Fatalf("picture %d: sent %v err %v", i, sent, err)
		}
	}
	began := time.Now()
	sent, err := s.SendFrame([]byte{9})
	if err != nil || sent {
		t.Fatalf("third picture: sent %v err %v, want not sent", sent, err)
	}
	if d := time.Since(began); d > 100*time.Millisecond {
		t.Errorf("took %v to refuse", d)
	}
}

func TestWindowReopensOnAck(t *testing.T) {
	s, car, _ := openBoth(t, fakecar.Options{MaxUnacked: 1})
	for i := 0; i < 10; i++ {
		eventually(t, "window to reopen", func() bool {
			sent, err := s.SendFrame([]byte{byte(i)})
			if err != nil {
				t.Fatal(err)
			}
			return sent
		})
	}
	eventually(t, "ten pictures", func() bool { return len(car.Frames()) == 10 })
}

func TestWindowDefaultsToOne(t *testing.T) {
	s, _, _ := openBoth(t, fakecar.Options{NoAck: true}) // the car leaves max_unacked out
	if sent, _ := s.SendFrame([]byte{1}); !sent {
		t.Fatal("first picture should be sent")
	}
	if sent, _ := s.SendFrame([]byte{2}); sent {
		t.Fatal("second picture should wait for an acknowledgement")
	}
}

func TestNoVideoSink(t *testing.T) {
	s, _ := start(t, fakecar.Options{NoVideo: true, TouchW: 800, TouchH: 480})
	if _, err := s.OpenVideo(); err == nil || !strings.Contains(err.Error(), "no usable video") {
		t.Fatalf("got %v", err)
	}
}

func TestOnlyOversizedVideo(t *testing.T) {
	s, _ := start(t, fakecar.Options{Video: []aaphone.VideoConfig{{Index: 0, Width: 2560, Height: 1440, FPS: 30}}, TouchW: 800, TouchH: 480})
	if _, err := s.OpenVideo(); err == nil || !strings.Contains(err.Error(), "no usable video") {
		t.Fatalf("got %v", err)
	}
}

func TestTouchesArrive(t *testing.T) {
	s, car, _ := openBoth(t, fakecar.Options{})
	car.Touch(100, 200, 0)
	car.Touch(100, 200, 1)
	want := []aaphone.Touch{{X: 100, Y: 200, Action: 0}, {X: 100, Y: 200, Action: 1}}
	for i, w := range want {
		select {
		case got := <-s.Touches():
			if got != w {
				t.Errorf("touch %d: got %+v want %+v", i, got, w)
			}
		case <-time.After(time.Second):
			t.Fatalf("touch %d never arrived", i)
		}
	}
}

func TestTouchFloodDoesNotStall(t *testing.T) {
	s, car, _ := openBoth(t, fakecar.Options{})
	for i := 0; i < 200; i++ { // more than the buffer holds, with nobody reading
		car.Touch(i, i, 2)
	}
	echo, err := car.Ping(7) // the session must still be answering
	if err != nil || echo != 7 {
		t.Fatalf("after flood: echo %d err %v", echo, err)
	}
	last := aaphone.Touch{}
	for drained := false; !drained; {
		select {
		case last = <-s.Touches():
		default:
			drained = true
		}
	}
	if last.X != 199 {
		t.Errorf("the newest touch should be kept, got %+v", last)
	}
}

func TestNoTouchscreen(t *testing.T) {
	s, _ := start(t, fakecar.Options{Video: twoSizes(), NoTouch: true})
	if err := s.OpenInput(); err == nil {
		t.Fatal("want an error when the car has no touchscreen")
	}
}

func TestProjectedInitially(t *testing.T) {
	s, _, _ := openBoth(t, fakecar.Options{})
	if !s.Projected() {
		t.Fatal("projected should be true while the car shows the phone's picture")
	}
}
