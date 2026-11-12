package aaphone

// videoState is the phone's view of the car's video stream.
type videoState struct {
	window    int
	projected bool
}

// handleChannel handles a message on a service channel; false means unhandled.
func (s *Session) handleChannel(ch uint8, id uint16, body []byte) bool { return false }
