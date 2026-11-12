package mode

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"dexd/aaphone"
	"dexd/screen"
)

// fakeClock fires timers only when the test advances it.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []fakeTimer
}

type fakeTimer struct {
	at time.Time
	ch chan time.Time
}

func newClock() *fakeClock { return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.timers = append(c.timers, fakeTimer{c.now.Add(d), ch})
	return ch
}

func (c *fakeClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

// advance moves time on one second at a time. Before each step it waits for
// Run to arm its next timer, which Run does only after finishing the
// previous tick, so every step is fully processed before the next.
func (c *fakeClock) advance(t *testing.T, seconds int) {
	t.Helper()
	for i := 0; i < seconds; i++ {
		deadline := time.Now().Add(2 * time.Second)
		for c.pending() == 0 {
			if time.Now().After(deadline) {
				t.Fatalf("Run armed no timer (step %d)", i)
			}
			time.Sleep(time.Millisecond)
		}
		c.mu.Lock()
		c.now = c.now.Add(time.Second)
		var keep []fakeTimer
		for _, tm := range c.timers {
			if !tm.at.After(c.now) {
				tm.ch <- c.now
			} else {
				keep = append(keep, tm)
			}
		}
		c.timers = keep
		c.mu.Unlock()
	}
	// let the last tick be handled
	deadline := time.Now().Add(2 * time.Second)
	for c.pending() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
}

// fakeCar records what Run sends. Touches is unbuffered, so a test's touch
// is handled before the test goes on.
type fakeCar struct {
	mu           sync.Mutex
	frames       int
	codecConfigs int
	byes         int
	projected    bool
	full         bool
	openVideoErr error
	touches      chan aaphone.Touch
	done         chan struct{}
	vc           aaphone.VideoConfig
}

func newCar() *fakeCar {
	return &fakeCar{
		projected: true,
		touches:   make(chan aaphone.Touch),
		done:      make(chan struct{}),
		vc:        aaphone.VideoConfig{Index: 1, Width: 1280, Height: 720, FPS: 30},
	}
}

func (f *fakeCar) OpenVideo() (aaphone.VideoConfig, error) { return f.vc, f.openVideoErr }
func (f *fakeCar) OpenInput() error                        { return nil }
func (f *fakeCar) SendCodecConfig([]byte) error {
	f.mu.Lock()
	f.codecConfigs++
	f.mu.Unlock()
	return nil
}
func (f *fakeCar) SendFrame([]byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.full {
		return false, nil
	}
	f.frames++
	return true, nil
}
func (f *fakeCar) Projected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.projected
}
func (f *fakeCar) setProjected(v bool) {
	f.mu.Lock()
	f.projected = v
	f.mu.Unlock()
}
func (f *fakeCar) Touches() <-chan aaphone.Touch { return f.touches }
func (f *fakeCar) Car() aaphone.Car {
	return aaphone.Car{TouchW: 1280, TouchH: 720, HasTouch: true, HasVideo: true, Video: []aaphone.VideoConfig{f.vc}}
}
func (f *fakeCar) Bye() error {
	f.mu.Lock()
	f.byes++
	f.mu.Unlock()
	return nil
}
func (f *fakeCar) Done() <-chan struct{} { return f.done }
func (f *fakeCar) count() (frames, configs, byes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.frames, f.codecConfigs, f.byes
}

type run struct {
	car    *fakeCar
	clock  *fakeClock
	result chan string
	mu     sync.Mutex
	status []Status
}

func startRun(t *testing.T, car *fakeCar) *run {
	t.Helper()
	r := &run{car: car, clock: newClock(), result: make(chan string, 1)}
	go func() {
		r.result <- Run(car, r.clock, func(s Status) {
			r.mu.Lock()
			r.status = append(r.status, s)
			r.mu.Unlock()
		})
	}()
	return r
}

// stateBecomes waits for the last reported state to equal want.
func (r *run) stateBecomes(t *testing.T, want string) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(time.Millisecond) {
		if r.last().State == want {
			return
		}
	}
	t.Fatalf("state %+v, want %q", r.last(), want)
}

func (r *run) last() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.status) == 0 {
		return Status{}
	}
	return r.status[len(r.status)-1]
}

func (r *run) wait(t *testing.T) string {
	t.Helper()
	select {
	case s := <-r.result:
		return s
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
		return ""
	}
}

func (r *run) stillRunning(t *testing.T) {
	t.Helper()
	select {
	case s := <-r.result:
		t.Fatalf("Run returned early: %q", s)
	case <-time.After(20 * time.Millisecond):
	}
}

// tap presses and releases at the centre of a button of the current screen.
func (r *run) tap(t *testing.T, s screen.Screen, button string) {
	t.Helper()
	b, ok := s.Buttons[button]
	if !ok {
		t.Fatalf("no button %q", button)
	}
	x, y := b.X+b.W/2, b.Y+b.H/2
	r.car.touches <- aaphone.Touch{X: x, Y: y, Action: 0}
	r.car.touches <- aaphone.Touch{X: x, Y: y, Action: 1}
}

func menu() screen.Screen {
	return screen.Menu(1280, 720, screen.Visible(screen.VideoSize{W: 1280, H: 720}), 20)
}

func placeholder() screen.Screen {
	return screen.Message(1280, 720, screen.Visible(screen.VideoSize{W: 1280, H: 720}), nil, true)
}

func TestTimeoutGoesToAndroidAuto(t *testing.T) {
	r := startRun(t, newCar())
	r.clock.advance(t, 19)
	r.stillRunning(t)
	r.clock.advance(t, 1)
	if got := r.wait(t); got != "menu timeout" {
		t.Fatalf("got %q", got)
	}
	if _, _, byes := r.car.count(); byes != 1 {
		t.Errorf("Bye called %d times", byes)
	}
	if r.last().State != "leaving" {
		t.Errorf("last status %+v", r.last())
	}
}

func TestTouchAndroidAuto(t *testing.T) {
	r := startRun(t, newCar())
	r.tap(t, menu(), "aa")
	if got := r.wait(t); got != "user chose Android Auto" {
		t.Fatalf("got %q", got)
	}
}

func TestSlideOffButtonIgnored(t *testing.T) {
	r := startRun(t, newCar())
	b := menu().Buttons["aa"]
	r.car.touches <- aaphone.Touch{X: b.X + b.W/2, Y: b.Y + b.H/2, Action: 0}
	r.car.touches <- aaphone.Touch{X: 1, Y: 1, Action: 1}
	r.stillRunning(t)
	r.stateBecomes(t, "menu")
}

func TestPressOutsideReleaseInsideIgnored(t *testing.T) {
	r := startRun(t, newCar())
	b := menu().Buttons["aa"]
	r.car.touches <- aaphone.Touch{X: 1, Y: 1, Action: 0}
	r.car.touches <- aaphone.Touch{X: b.X + b.W/2, Y: b.Y + b.H/2, Action: 1}
	r.stillRunning(t)
}

func TestSecondFingerIgnored(t *testing.T) {
	r := startRun(t, newCar())
	b := menu().Buttons["aa"]
	r.car.touches <- aaphone.Touch{X: b.X + b.W/2, Y: b.Y + b.H/2, Action: 5}
	r.car.touches <- aaphone.Touch{X: b.X + b.W/2, Y: b.Y + b.H/2, Action: 6}
	r.stillRunning(t)
}

func TestTouchOutsideScreenIgnored(t *testing.T) {
	r := startRun(t, newCar())
	r.car.touches <- aaphone.Touch{X: 99999, Y: 99999, Action: 0}
	r.car.touches <- aaphone.Touch{X: 99999, Y: 99999, Action: 1}
	r.stillRunning(t)
}

func TestDexShowsPlaceholderThenBack(t *testing.T) {
	r := startRun(t, newCar())
	r.clock.advance(t, 5)
	r.tap(t, menu(), "dex")
	r.stateBecomes(t, "placeholder")
	r.tap(t, placeholder(), "back")
	r.stateBecomes(t, "menu")
	r.clock.advance(t, 19) // countdown restarted at 20
	r.stillRunning(t)
	r.clock.advance(t, 1)
	if got := r.wait(t); got != "menu timeout" {
		t.Fatalf("got %q", got)
	}
}

func TestPlaceholderTimesOut(t *testing.T) {
	r := startRun(t, newCar())
	r.tap(t, menu(), "dex")
	r.clock.advance(t, 14)
	r.stateBecomes(t, "placeholder")
	r.clock.advance(t, 1)
	r.stateBecomes(t, "menu")
}

func TestCarEndsSession(t *testing.T) {
	car := newCar()
	r := startRun(t, car)
	close(car.done)
	if got := r.wait(t); got != "car ended the session" {
		t.Fatalf("got %q", got)
	}
	if _, _, byes := car.count(); byes != 0 {
		t.Errorf("Bye called %d times", byes)
	}
}

func TestNothingUsable(t *testing.T) {
	car := newCar()
	car.openVideoErr = errors.New("no usable video")
	r := startRun(t, car)
	if got := r.wait(t); !strings.HasPrefix(got, "car offers nothing usable") {
		t.Fatalf("got %q", got)
	}
	if _, _, byes := car.count(); byes != 1 {
		t.Errorf("Bye called %d times", byes)
	}
}

func TestFrameResentEverySecond(t *testing.T) {
	car := newCar()
	r := startRun(t, car)
	r.clock.advance(t, 3)
	frames, configs, _ := car.count()
	if frames < 3 || configs != 1 {
		t.Fatalf("frames %d configs %d", frames, configs)
	}
}

func TestWindowFullKeepsRunning(t *testing.T) {
	car := newCar()
	car.full = true
	r := startRun(t, car)
	r.clock.advance(t, 20)
	if got := r.wait(t); got != "menu timeout" {
		t.Fatalf("got %q", got)
	}
}

func TestNotProjectedPauses(t *testing.T) {
	car := newCar()
	car.setProjected(false)
	r := startRun(t, car)
	r.clock.advance(t, 30)
	r.stillRunning(t)
	if frames, _, _ := car.count(); frames != 0 {
		t.Fatalf("%d frames sent while not projected", frames)
	}
	car.setProjected(true)
	r.clock.advance(t, 20) // first tick notices projection; countdown restarts at 20
	r.stillRunning(t)
	r.clock.advance(t, 1)
	if got := r.wait(t); got != "menu timeout" {
		t.Fatalf("got %q", got)
	}
}

func TestNeverShownLeaves(t *testing.T) {
	car := newCar()
	car.setProjected(false)
	r := startRun(t, car)
	r.clock.advance(t, 59)
	r.stillRunning(t)
	r.clock.advance(t, 1)
	if got := r.wait(t); got != "car never showed the menu" {
		t.Fatalf("got %q", got)
	}
}
