package aaphone

import (
	"bytes"
	"reflect"
	"testing"

	"dexd/pb"
)

func videoConfig(res, fps, mw, mh uint64) []byte {
	var w pb.Writer
	w.Varint(1, res)
	w.Varint(2, fps)
	if mw != 0 {
		w.Varint(3, mw)
	}
	if mh != 0 {
		w.Varint(4, mh)
	}
	return w.Out()
}

func TestParseCarServices(t *testing.T) {
	var touch, input, svc1 pb.Writer
	touch.Varint(1, 800)
	touch.Varint(2, 480)
	input.Bytes(2, touch.Out())
	svc1.Varint(1, 1)
	svc1.Bytes(4, input.Out())

	var sink, svc3 pb.Writer
	sink.Varint(1, 3) // H.264
	sink.Bytes(4, videoConfig(1, 2, 0, 0))
	sink.Bytes(4, videoConfig(2, 2, 0, 40))
	svc3.Varint(1, 3)
	svc3.Bytes(3, sink.Out())

	var audio, svc4 pb.Writer
	audio.Varint(1, 1) // PCM
	svc4.Varint(1, 4)
	svc4.Bytes(3, audio.Out())

	var second, svc5 pb.Writer
	second.Varint(1, 3)
	second.Bytes(4, videoConfig(3, 2, 0, 0))
	second.Varint(7, 1) // not the main display
	svc5.Varint(1, 5)
	svc5.Bytes(3, second.Out())

	var resp pb.Writer
	resp.Bytes(1, svc1.Out())
	resp.Bytes(1, svc3.Out())
	resp.Bytes(1, svc4.Out())
	resp.Bytes(1, svc5.Out())
	resp.String(2, "Genesis")
	resp.String(3, "G80")

	car, err := parseServiceDiscoveryResponse(resp.Out())
	if err != nil {
		t.Fatal(err)
	}
	want := Car{
		Video:        []VideoConfig{{0, 800, 480, 30, 0, 0}, {1, 1280, 720, 30, 0, 40}},
		VideoChannel: 3, HasVideo: true,
		InputChannel: 1, TouchW: 800, TouchH: 480, HasTouch: true,
		Make: "Genesis", Model: "G80",
	}
	if !reflect.DeepEqual(car, want) {
		t.Fatalf("got  %+v\nwant %+v", car, want)
	}
}

func TestParseCarServicesKeepsIndexOfKnownSizes(t *testing.T) {
	var sink, svc, resp pb.Writer
	sink.Varint(1, 3)
	sink.Bytes(4, videoConfig(4, 2, 0, 0)) // 2560x1440: not usable
	sink.Bytes(4, videoConfig(2, 1, 0, 0)) // 1280x720 at 60
	svc.Varint(1, 2)
	svc.Bytes(3, sink.Out())
	resp.Bytes(1, svc.Out())
	car, err := parseServiceDiscoveryResponse(resp.Out())
	if err != nil {
		t.Fatal(err)
	}
	if len(car.Video) != 1 || car.Video[0] != (VideoConfig{1, 1280, 720, 60, 0, 0}) {
		t.Fatalf("got %+v", car.Video)
	}
}

func TestParseTouchReport(t *testing.T) {
	var p, ev, rep pb.Writer
	p.Varint(1, 120)
	p.Varint(2, 300)
	p.Varint(3, 0)
	ev.Bytes(1, p.Out())
	ev.Varint(2, 0)
	ev.Varint(3, 0)
	rep.Varint(1, 123456)
	rep.Bytes(3, ev.Out())
	got, err := parseInputReport(rep.Out())
	if err != nil || !reflect.DeepEqual(got, []Touch{{120, 300, 0, 0}}) {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestParseInputReportWithoutTouch(t *testing.T) {
	var rep pb.Writer
	rep.Varint(1, 1)
	got, err := parseInputReport(rep.Out())
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestBuildChannelOpen(t *testing.T) {
	if got := buildChannelOpen(3); !bytes.Equal(got, []byte{0x08, 0x00, 0x10, 0x03}) {
		t.Fatalf("got % x", got)
	}
}

func TestParseConfig(t *testing.T) {
	var w pb.Writer
	w.Varint(1, 2)
	w.Varint(2, 4)
	w.Varint(3, 1)
	w.Varint(3, 0)
	ready, max, idx, err := parseConfig(w.Out())
	if err != nil || !ready || max != 4 || !reflect.DeepEqual(idx, []int{1, 0}) {
		t.Fatalf("ready %v max %d idx %v err %v", ready, max, idx, err)
	}
}

func TestMessageIDPrefix(t *testing.T) {
	m := message(MsgStart, []byte{1, 2})
	if !bytes.Equal(m, []byte{0x80, 0x01, 1, 2}) {
		t.Fatalf("got % x", m)
	}
	id, body, err := splitMessage(m)
	if err != nil || id != MsgStart || !bytes.Equal(body, []byte{1, 2}) {
		t.Fatalf("id %#x body % x err %v", id, body, err)
	}
	if _, _, err := splitMessage([]byte{1}); err == nil {
		t.Error("one-byte message: want error")
	}
}
