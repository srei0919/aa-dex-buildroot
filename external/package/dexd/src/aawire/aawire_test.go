package aawire

import (
	"bytes"
	"testing"
	"testing/iotest"
)

func TestWriteSingle(t *testing.T) {
	var b bytes.Buffer
	if err := WriteFrame(&b, Frame{Channel: 3, Flags: 0x0b, Payload: []byte("hello")}); err != nil {
		t.Fatal(err)
	}
	want := []byte{0x03, 0x0b, 0x00, 0x05, 'h', 'e', 'l', 'l', 'o'}
	if !bytes.Equal(b.Bytes(), want) {
		t.Fatalf("got % x want % x", b.Bytes(), want)
	}
}

func TestSplitLarge(t *testing.T) {
	msg := make([]byte, 40000)
	for i := range msg {
		msg[i] = byte(i)
	}
	fs := Split(5, FlagEncrypted, msg)
	if len(fs) != 3 {
		t.Fatalf("frames = %d, want 3", len(fs))
	}
	sizes := []int{16384, 16384, 7232}
	flags := []uint8{FlagEncrypted | FlagFirst, FlagEncrypted, FlagEncrypted | FlagLast}
	for i, f := range fs {
		if len(f.Payload) != sizes[i] || f.Flags != flags[i] || f.Channel != 5 {
			t.Errorf("frame %d: len %d flags %#x ch %d", i, len(f.Payload), f.Flags, f.Channel)
		}
	}
	if fs[0].Total != 40000 || fs[1].Total != 0 || fs[2].Total != 0 {
		t.Errorf("totals = %d %d %d", fs[0].Total, fs[1].Total, fs[2].Total)
	}
}

func TestSplitSmall(t *testing.T) {
	fs := Split(0, FlagEncrypted, make([]byte, 10))
	if len(fs) != 1 || fs[0].Flags != FlagEncrypted|FlagFirst|FlagLast || fs[0].Total != 0 {
		t.Fatalf("got %+v", fs)
	}
}

func TestReadOneByteAtATime(t *testing.T) {
	var b bytes.Buffer
	in := []Frame{
		{Channel: 1, Flags: FlagFirst, Total: 70000, Payload: []byte{1, 2, 3}},
		{Channel: 2, Flags: FlagFirst | FlagLast | FlagEncrypted, Payload: []byte{9}},
	}
	for _, f := range in {
		if err := WriteFrame(&b, f); err != nil {
			t.Fatal(err)
		}
	}
	r := iotest.OneByteReader(&b)
	for i, want := range in {
		got, err := ReadFrame(r)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if got.Channel != want.Channel || got.Flags != want.Flags || got.Total != want.Total || !bytes.Equal(got.Payload, want.Payload) {
			t.Errorf("frame %d: got %+v want %+v", i, got, want)
		}
	}
}

func TestAssembleInterleaved(t *testing.T) {
	a := bytes.Repeat([]byte{0xaa}, 40000)
	b := bytes.Repeat([]byte{0xbb}, 20000)
	fa, fb := Split(1, 0, a), Split(2, 0, b)
	var asm Assembler
	got := map[uint8][]byte{}
	feed := func(f Frame) {
		ch, msg, done, err := asm.Add(f)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			got[ch] = msg
		}
	}
	feed(fa[0])
	feed(fb[0])
	feed(fa[1])
	feed(fb[1])
	feed(fa[2])
	if !bytes.Equal(got[1], a) || !bytes.Equal(got[2], b) {
		t.Fatalf("lens %d %d", len(got[1]), len(got[2]))
	}
}

func TestAssembleMiddleWithoutFirst(t *testing.T) {
	var asm Assembler
	if _, _, _, err := asm.Add(Frame{Channel: 4, Flags: 0, Payload: []byte{1}}); err == nil {
		t.Fatal("want error for a middle fragment with no start")
	}
	ch, msg, done, err := asm.Add(Frame{Channel: 4, Flags: FlagFirst | FlagLast, Payload: []byte{7, 8}})
	if err != nil || !done || ch != 4 || !bytes.Equal(msg, []byte{7, 8}) {
		t.Fatalf("got ch %d msg % x done %v err %v", ch, msg, done, err)
	}
}
