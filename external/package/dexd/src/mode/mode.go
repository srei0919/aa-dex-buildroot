// Package mode is dexd's state machine for milestone 1: the menu on the car
// screen, the DeX placeholder, and the hand-over to normal Android Auto.
package mode

import (
	"fmt"
	"time"

	"dexd/aaphone"
	"dexd/h264"
	"dexd/screen"
)

// Car is what Run needs from a session; *aaphone.Session satisfies it.
type Car interface {
	OpenVideo() (aaphone.VideoConfig, error)
	OpenInput() error
	SendCodecConfig([]byte) error
	SendFrame([]byte) (bool, error)
	Projected() bool
	Touches() <-chan aaphone.Touch
	Car() aaphone.Car
	Bye() error
	Done() <-chan struct{}
}

// Clock is time, replaceable in tests.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// RealClock is the system clock.
type RealClock struct{}

func (RealClock) Now() time.Time                         { return time.Now() }
func (RealClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Status is reported on every change. State is "menu", "placeholder" or "leaving".
type Status struct {
	State  string
	Reason string
	Video  aaphone.VideoConfig
}

const (
	menuSeconds        = 20
	placeholderSeconds = 15
	maxHiddenSeconds   = 60
)

var placeholderLines = []string{"DeX is not in this version yet.", "It arrives with the next update."}

// Run shows the menu and returns why it stopped. Before returning it says
// goodbye to the car, unless the car already ended the session.
func Run(car Car, clock Clock, report func(Status)) (reason string) {
	m := &machine{car: car, report: report}
	return m.run(clock)
}

type machine struct {
	car    Car
	report func(Status)

	vc      aaphone.VideoConfig
	visible screen.Rect
	bg      [3]byte

	state     string
	left      int // seconds left in the current state
	cur       screen.Screen
	encoded   [2][]byte // the current screen with idr_pic_id 0 and 1
	idr       int
	hidden    bool   // the car is not showing our picture
	hiddenFor int    // consecutive seconds hidden
	pressedOn string // button under the finger that went down, "" if none
	pressed   bool
}

func (m *machine) leave(reason string, bye bool) string {
	if bye {
		m.car.Bye()
	}
	m.report(Status{State: "leaving", Reason: reason, Video: m.vc})
	return reason
}

func (m *machine) run(clock Clock) string {
	vc, err := m.car.OpenVideo()
	if err != nil {
		return m.leave("car offers nothing usable: "+err.Error(), true)
	}
	if err := m.car.OpenInput(); err != nil {
		return m.leave("car offers nothing usable: "+err.Error(), true)
	}
	m.vc = vc
	m.visible = screen.Visible(screen.VideoSize{W: vc.Width, H: vc.Height, MarginW: vc.MarginW, MarginH: vc.MarginH})
	m.bg = h264.YCbCr(screen.Background[0], screen.Background[1], screen.Background[2])
	if err := m.car.SendCodecConfig(h264.Params(vc.Width, vc.Height)); err != nil {
		return m.leave("sending to the car failed: "+err.Error(), true)
	}

	m.enter("menu")
	m.hidden = !m.car.Projected()
	if err := m.show(); err != nil {
		return m.leave("sending to the car failed: "+err.Error(), true)
	}

	tick := clock.After(time.Second)
	for {
		select {
		case <-m.car.Done():
			return m.leave("car ended the session", false)

		case t := <-m.car.Touches():
			if choice := m.touch(t); choice != "" {
				switch choice {
				case "aa":
					return m.leave("user chose Android Auto", true)
				case "dex":
					m.enter("placeholder")
				case "back":
					m.enter("menu")
				}
				if err := m.show(); err != nil {
					return m.leave("sending to the car failed: "+err.Error(), true)
				}
			}

		case <-tick:
			if done, reason := m.second(); done {
				return m.leave(reason, true)
			}
			// Armed only after the tick is handled (tests rely on this).
			tick = clock.After(time.Second)
		}
	}
}

// enter switches state and restarts its countdown.
func (m *machine) enter(state string) {
	m.state = state
	m.left = menuSeconds
	if state == "placeholder" {
		m.left = placeholderSeconds
	}
	m.pressed = false
	m.redraw()
	m.report(Status{State: state, Video: m.vc})
}

func (m *machine) redraw() {
	switch m.state {
	case "menu":
		m.cur = screen.Menu(m.vc.Width, m.vc.Height, m.visible, m.left)
	default:
		m.cur = screen.Message(m.vc.Width, m.vc.Height, m.visible, placeholderLines, true)
	}
	m.encoded = [2][]byte{}
}

// show sends the current screen, unless the car is not showing our picture.
// A full acknowledgement window is not an error: the next second resends.
func (m *machine) show() error {
	if m.hidden {
		return nil
	}
	// Consecutive pictures need different idr_pic_id values.
	id := m.idr % 2
	m.idr++
	if m.encoded[id] == nil {
		c := m.cur.Canvas
		m.encoded[id] = h264.EncodeStill(h264.FromRGB(c.W, c.H, c.Pix), m.bg, id)
	}
	pic := m.encoded[id]
	_, err := m.car.SendFrame(pic)
	return err
}

// second handles one tick of the clock.
func (m *machine) second() (done bool, reason string) {
	if !m.car.Projected() {
		m.hidden = true
		// Never wait forever on a car that does not show us: a car that
		// answers pings would otherwise keep dexd (and no Android Auto) alive.
		if m.hiddenFor++; m.hiddenFor >= maxHiddenSeconds {
			return true, "car never showed the menu"
		}
		return false, ""
	}
	m.hiddenFor = 0
	if m.hidden {
		// The car shows our picture again: start the countdown afresh.
		m.hidden = false
		m.enter(m.state)
	} else {
		m.left--
		if m.left <= 0 {
			if m.state == "menu" {
				return true, "menu timeout"
			}
			m.enter("menu")
		} else if m.state == "menu" {
			m.redraw()
		}
	}
	if err := m.show(); err != nil {
		return true, "sending to the car failed: " + err.Error()
	}
	return false, ""
}

// touch returns the button chosen by a press and release inside the same
// button, or "". Moves and extra fingers are ignored.
func (m *machine) touch(t aaphone.Touch) string {
	cc := m.car.Car()
	x, y := screen.ScaleTouch(t.X, t.Y, cc.TouchW, cc.TouchH, m.vc.Width, m.vc.Height)
	switch t.Action {
	case 0:
		m.pressedOn, m.pressed = m.cur.Hit(x, y), true
	case 1:
		if !m.pressed {
			return ""
		}
		m.pressed = false
		if b := m.cur.Hit(x, y); b != "" && b == m.pressedOn {
			return b
		}
	}
	return ""
}

func (s Status) String() string { return fmt.Sprintf("%s %s", s.State, s.Reason) }
