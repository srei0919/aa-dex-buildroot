// Package aaphone is the phone side of an Android Auto session: it talks to
// a car (through aa-proxy-rs) the way a phone would.
package aaphone

import (
	"encoding/binary"
	"errors"

	"dexd/pb"
)

// Message ids. A message is [id u16 BE][protobuf body].
const (
	// control channel (0)
	MsgVersionRequest      uint16 = 1
	MsgVersionResponse     uint16 = 2
	MsgSSL                 uint16 = 3
	MsgAuthComplete        uint16 = 4
	MsgSDRequest           uint16 = 5
	MsgSDResponse          uint16 = 6
	MsgChannelOpenRequest  uint16 = 7
	MsgChannelOpenResponse uint16 = 8
	MsgPingRequest         uint16 = 11
	MsgPingResponse        uint16 = 12
	MsgByeByeRequest       uint16 = 15
	MsgByeByeResponse      uint16 = 16

	// media channels
	MsgData       uint16 = 0
	MsgCodecData  uint16 = 1
	MsgSetup      uint16 = 0x8000
	MsgStart      uint16 = 0x8001
	MsgStop       uint16 = 0x8002
	MsgConfig     uint16 = 0x8003
	MsgAck        uint16 = 0x8004
	MsgVideoFocus uint16 = 0x8008

	// input channel
	MsgInputReport        uint16 = 0x8001
	MsgKeyBindingRequest  uint16 = 0x8002
	MsgKeyBindingResponse uint16 = 0x8003
)

// CodecH264 is the media codec type for baseline H.264 video.
const CodecH264 = 3

// VideoConfig is one picture format the car accepts. Index is its position
// in the car's list, which is what a stream start refers to. Margins are the
// total unused pixels in each direction.
type VideoConfig struct {
	Index, Width, Height, FPS, MarginW, MarginH int
}

// Car is what the car said about itself in service discovery.
type Car struct {
	Video        []VideoConfig
	VideoChannel uint8
	HasVideo     bool

	InputChannel   uint8
	TouchW, TouchH int
	HasTouch       bool

	Make, Model string
}

// Touch is one pointer of a touchscreen event, in touchscreen coordinates.
// Action: 0 down, 1 up, 2 move, 5 extra pointer down, 6 extra pointer up.
type Touch struct {
	X, Y, Pointer, Action int
}

func message(id uint16, body []byte) []byte {
	return append(binary.BigEndian.AppendUint16(make([]byte, 0, 2+len(body)), id), body...)
}

func splitMessage(m []byte) (id uint16, body []byte, err error) {
	if len(m) < 2 {
		return 0, nil, errors.New("aaphone: message shorter than its id")
	}
	return binary.BigEndian.Uint16(m), m[2:], nil
}

func buildServiceDiscoveryRequest(name string) []byte {
	var w pb.Writer
	w.String(4, name)
	w.String(5, name)
	return w.Out()
}

var resolutions = map[uint64][2]int{1: {800, 480}, 2: {1280, 720}, 3: {1920, 1080}}

func parseServiceDiscoveryResponse(b []byte) (Car, error) {
	var car Car
	top, err := pb.Parse(b)
	if err != nil {
		return car, err
	}
	for _, f := range top {
		switch {
		case f.Num == 1 && f.Wire == pb.WireBytes:
			if err := parseService(f.Data, &car); err != nil {
				return car, err
			}
		case f.Num == 2 && f.Wire == pb.WireBytes && car.Make == "":
			car.Make = string(f.Data)
		case f.Num == 3 && f.Wire == pb.WireBytes && car.Model == "":
			car.Model = string(f.Data)
		case f.Num == 17 && f.Wire == pb.WireBytes: // head unit info: make = 1, model = 2
			if info, err := pb.Parse(f.Data); err == nil {
				for _, i := range info {
					if i.Num == 1 && i.Wire == pb.WireBytes {
						car.Make = string(i.Data)
					}
					if i.Num == 2 && i.Wire == pb.WireBytes {
						car.Model = string(i.Data)
					}
				}
			}
		}
	}
	return car, nil
}

func parseService(b []byte, car *Car) error {
	fs, err := pb.Parse(b)
	if err != nil {
		return err
	}
	var id uint64
	for _, f := range fs {
		if f.Num == 1 && f.Wire == pb.WireVarint {
			id = f.Val
		}
	}
	for _, f := range fs {
		if f.Wire != pb.WireBytes {
			continue
		}
		switch f.Num {
		case 3: // media sink
			if car.HasVideo {
				continue
			}
			video, ok, err := parseVideoSink(f.Data)
			if err != nil {
				return err
			}
			if ok {
				car.Video, car.VideoChannel, car.HasVideo = video, uint8(id), true
			}
		case 4: // input source
			if car.HasTouch {
				continue
			}
			w, h, ok, err := parseTouchscreen(f.Data)
			if err != nil {
				return err
			}
			if ok {
				car.TouchW, car.TouchH, car.InputChannel, car.HasTouch = w, h, uint8(id), true
			}
		}
	}
	return nil
}

// parseVideoSink reports ok for an H.264 sink on the main display.
func parseVideoSink(b []byte) (video []VideoConfig, ok bool, err error) {
	fs, err := pb.Parse(b)
	if err != nil {
		return nil, false, err
	}
	codec, display := uint64(1), uint64(0)
	index := 0
	for _, f := range fs {
		switch {
		case f.Num == 1 && f.Wire == pb.WireVarint:
			codec = f.Val
		case f.Num == 7 && f.Wire == pb.WireVarint:
			display = f.Val
		case f.Num == 4 && f.Wire == pb.WireBytes:
			if vc, usable := parseVideoConfig(f.Data, index); usable {
				video = append(video, vc)
			}
			index++
		}
	}
	if codec != CodecH264 || display != 0 {
		return nil, false, nil
	}
	return video, true, nil
}

func parseVideoConfig(b []byte, index int) (VideoConfig, bool) {
	fs, err := pb.Parse(b)
	if err != nil {
		return VideoConfig{}, false
	}
	vc := VideoConfig{Index: index, FPS: 30}
	known := false
	for _, f := range fs {
		if f.Wire != pb.WireVarint {
			continue
		}
		switch f.Num {
		case 1:
			if wh, ok := resolutions[f.Val]; ok {
				vc.Width, vc.Height, known = wh[0], wh[1], true
			}
		case 2:
			if f.Val == 1 {
				vc.FPS = 60
			}
		case 3:
			vc.MarginW = int(f.Val)
		case 4:
			vc.MarginH = int(f.Val)
		}
	}
	return vc, known
}

func parseTouchscreen(b []byte) (w, h int, ok bool, err error) {
	fs, err := pb.Parse(b)
	if err != nil {
		return 0, 0, false, err
	}
	for _, f := range fs {
		if f.Num != 2 || f.Wire != pb.WireBytes {
			continue
		}
		ts, err := pb.Parse(f.Data)
		if err != nil {
			return 0, 0, false, err
		}
		for _, t := range ts {
			if t.Num == 1 {
				w = int(t.Val)
			}
			if t.Num == 2 {
				h = int(t.Val)
			}
		}
		if w > 0 && h > 0 {
			return w, h, true, nil
		}
	}
	return 0, 0, false, nil
}

func buildChannelOpen(service int) []byte {
	var w pb.Writer
	w.Sint(1, 0)
	w.Varint(2, uint64(service))
	return w.Out()
}

// firstVarint returns the value of a top-level varint field, or def.
func firstVarint(b []byte, field int, def int64) (int64, error) {
	fs, err := pb.Parse(b)
	if err != nil {
		return def, err
	}
	for _, f := range fs {
		if f.Num == field && f.Wire == pb.WireVarint {
			return int64(f.Val), nil
		}
	}
	return def, nil
}

func varintMessage(field int, v uint64) []byte {
	var w pb.Writer
	w.Varint(field, v)
	return w.Out()
}

func buildPingResponse(timestamp int64) []byte { return varintMessage(1, uint64(timestamp)) }
func buildByeBye(reason int) []byte            { return varintMessage(1, uint64(reason)) }
func buildSetup(codec int) []byte              { return varintMessage(1, uint64(codec)) }

func buildStart(session, configIndex int) []byte {
	var w pb.Writer
	w.Varint(1, uint64(session))
	w.Varint(2, uint64(configIndex))
	return w.Out()
}

func buildKeyBinding() []byte {
	var w pb.Writer
	w.Bytes(1, nil)
	return w.Out()
}

func parseConfig(b []byte) (ready bool, maxUnacked int, indices []int, err error) {
	fs, err := pb.Parse(b)
	if err != nil {
		return false, 0, nil, err
	}
	for _, f := range fs {
		switch {
		case f.Num == 1 && f.Wire == pb.WireVarint:
			ready = f.Val == 2
		case f.Num == 2 && f.Wire == pb.WireVarint:
			maxUnacked = int(f.Val)
		case f.Num == 3 && f.Wire == pb.WireVarint:
			indices = append(indices, int(f.Val))
		case f.Num == 3 && f.Wire == pb.WireBytes:
			for _, v := range pb.PackedVarints(f.Data) {
				indices = append(indices, int(v))
			}
		}
	}
	return ready, maxUnacked, indices, nil
}

func parseInputReport(b []byte) ([]Touch, error) {
	fs, err := pb.Parse(b)
	if err != nil {
		return nil, err
	}
	var out []Touch
	for _, f := range fs {
		if f.Num != 3 || f.Wire != pb.WireBytes {
			continue
		}
		ev, err := pb.Parse(f.Data)
		if err != nil {
			return nil, err
		}
		action := 0
		var pointers []Touch
		for _, e := range ev {
			switch {
			case e.Num == 3 && e.Wire == pb.WireVarint:
				action = int(e.Val)
			case e.Num == 1 && e.Wire == pb.WireBytes:
				ps, err := pb.Parse(e.Data)
				if err != nil {
					return nil, err
				}
				var t Touch
				for _, p := range ps {
					switch p.Num {
					case 1:
						t.X = int(p.Val)
					case 2:
						t.Y = int(p.Val)
					case 3:
						t.Pointer = int(p.Val)
					}
				}
				pointers = append(pointers, t)
			}
		}
		for i := range pointers {
			pointers[i].Action = action
		}
		out = append(out, pointers...)
	}
	return out, nil
}
