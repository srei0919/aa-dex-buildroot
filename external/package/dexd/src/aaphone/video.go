package aaphone

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"dexd/aawire"
)

// videoState is the phone's view of the car's video stream.
type videoState struct {
	mu        sync.Mutex
	window    int // pictures the car lets us have unacknowledged
	unacked   int
	projected bool
	started   time.Time
}

const replyTimeout = 5 * time.Second

// ChooseVideo picks the picture format to use: the largest one that is at
// most 1920x1080, and for the same size the 30 fps entry over the 60 fps one.
// When allowed is not empty only those list positions are considered.
func ChooseVideo(cfgs []VideoConfig, allowed []int) (VideoConfig, bool) {
	var best VideoConfig
	found := false
	for _, c := range cfgs {
		if c.Width <= 0 || c.Height <= 0 || c.Width > 1920 || c.Height > 1080 {
			continue
		}
		if len(allowed) > 0 && !contains(allowed, c.Index) {
			continue
		}
		switch {
		case !found, c.Width*c.Height > best.Width*best.Height:
			best, found = c, true
		case c.Width*c.Height == best.Width*best.Height && best.FPS != 30 && c.FPS == 30:
			best = c
		}
	}
	return best, found
}

func contains(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// openChannel asks the car to open the channel of one of its services.
func (s *Session) openChannel(service uint8) error {
	reply := s.expect(waitKey{service, true, MsgChannelOpenResponse})
	if err := s.send(service, aawire.FlagControl, MsgChannelOpenRequest, buildChannelOpen(int(service))); err != nil {
		return err
	}
	body, err := s.await(reply, "channel open reply", replyTimeout)
	if err != nil {
		return err
	}
	if status, err := firstVarint(body, 1, 0); err != nil || status != 0 {
		return fmt.Errorf("car refused to open channel %d (status %d)", service, status)
	}
	return nil
}

// OpenVideo opens the car's video channel, picks a picture format and starts
// the stream. Every picture sent afterwards must have the returned size.
func (s *Session) OpenVideo() (VideoConfig, error) {
	if !s.car.HasVideo {
		return VideoConfig{}, errors.New("no usable video: the car offers no H.264 picture on its main screen")
	}
	if _, ok := ChooseVideo(s.car.Video, nil); !ok {
		return VideoConfig{}, fmt.Errorf("no usable video: none of the car's sizes is 1920x1080 or smaller (%+v)", s.car.Video)
	}
	ch := s.car.VideoChannel
	if err := s.openChannel(ch); err != nil {
		return VideoConfig{}, err
	}
	reply := s.expect(waitKey{ch, false, MsgConfig})
	if err := s.send(ch, 0, MsgSetup, buildSetup(CodecH264)); err != nil {
		return VideoConfig{}, err
	}
	body, err := s.await(reply, "video configuration", replyTimeout)
	if err != nil {
		return VideoConfig{}, err
	}
	ready, maxUnacked, allowed, err := parseConfig(body)
	if err != nil {
		return VideoConfig{}, err
	}
	if !ready {
		return VideoConfig{}, errors.New("the car answered that its video is not ready")
	}
	choice, ok := ChooseVideo(s.car.Video, allowed)
	if !ok {
		// The car's shortlist names nothing we can use; fall back to its full list.
		s.log.Printf("car's preferred video entries %v are unusable; choosing from the full list", allowed)
		choice, _ = ChooseVideo(s.car.Video, nil)
	}
	s.video.mu.Lock()
	s.video.window = 1
	if maxUnacked > 0 {
		s.video.window = maxUnacked
	}
	s.video.unacked = 0
	s.video.started = time.Now()
	s.video.mu.Unlock()
	s.log.Printf("video: chose %+v, window %d, car shortlist %v", choice, s.video.window, allowed)
	return choice, s.send(ch, 0, MsgStart, buildStart(1, choice.Index))
}

// SendCodecConfig sends the stream parameters (SPS and PPS, Annex B).
func (s *Session) SendCodecConfig(spspps []byte) error {
	return s.send(s.car.VideoChannel, 0, MsgCodecData, spspps)
}

// SendFrame sends one picture (an H.264 access unit, Annex B). It never
// waits for the car: when the car has not acknowledged enough earlier
// pictures it returns sent=false and the picture is not sent.
func (s *Session) SendFrame(au []byte) (sent bool, err error) {
	s.video.mu.Lock()
	if s.video.unacked >= s.video.window {
		s.video.mu.Unlock()
		return false, nil
	}
	s.video.unacked++
	micros := uint64(time.Since(s.video.started) / time.Microsecond)
	s.video.mu.Unlock()

	body := make([]byte, 8, 8+len(au))
	binary.BigEndian.PutUint64(body, micros)
	return true, s.send(s.car.VideoChannel, 0, MsgData, append(body, au...))
}

// Projected reports whether the car is showing the phone's picture.
func (s *Session) Projected() bool {
	s.video.mu.Lock()
	defer s.video.mu.Unlock()
	return s.video.projected
}

// handleChannel handles a message on a service channel; false means unhandled.
func (s *Session) handleChannel(ch uint8, id uint16, body []byte) bool {
	switch {
	case s.car.HasVideo && ch == s.car.VideoChannel:
		return s.handleVideo(id, body)
	case s.car.HasTouch && ch == s.car.InputChannel:
		return s.handleInput(id, body)
	}
	return false
}

func (s *Session) handleVideo(id uint16, body []byte) bool {
	switch id {
	case MsgAck:
		n, _ := firstVarint(body, 2, 1)
		s.video.mu.Lock()
		// A car may also acknowledge the stream parameters, which are not
		// counted against the window; never let the count go negative.
		if s.video.unacked -= int(n); s.video.unacked < 0 {
			s.video.unacked = 0
		}
		s.video.mu.Unlock()
	case MsgVideoFocus:
		focus, _ := firstVarint(body, 1, 1)
		s.video.mu.Lock()
		s.video.projected = focus == 1
		s.video.mu.Unlock()
		s.log.Printf("car video focus: %d", focus)
	case MsgConfig:
		s.log.Printf("car sent a video configuration nobody was waiting for")
	default:
		return false
	}
	return true
}
