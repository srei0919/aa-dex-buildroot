package aaphone

import "errors"

// OpenInput opens the car's touchscreen channel. Touches then arrive on
// Touches().
func (s *Session) OpenInput() error {
	if !s.car.HasTouch {
		return errors.New("the car has no touchscreen")
	}
	if err := s.openChannel(s.car.InputChannel); err != nil {
		return err
	}
	return s.send(s.car.InputChannel, 0, MsgKeyBindingRequest, buildKeyBinding())
}

// Touches delivers touchscreen events in the car's touchscreen coordinates.
// It holds 64 events; when nobody reads, the oldest are dropped.
func (s *Session) Touches() <-chan Touch { return s.touch }

func (s *Session) handleInput(id uint16, body []byte) bool {
	switch id {
	case MsgInputReport:
		touches, err := parseInputReport(body)
		if err != nil {
			s.log.Printf("unreadable input report: %v", err)
			return true
		}
		for _, t := range touches {
			s.pushTouch(t)
		}
	case MsgKeyBindingResponse:
	default:
		return false
	}
	return true
}

// pushTouch never blocks the reader: a full buffer loses its oldest event.
func (s *Session) pushTouch(t Touch) {
	for {
		select {
		case s.touch <- t:
			return
		default:
		}
		select {
		case <-s.touch:
		default:
		}
	}
}
