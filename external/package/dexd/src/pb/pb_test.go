package pb

import (
	"bytes"
	"testing"
)

func TestVarintRoundTrip(t *testing.T) {
	for _, v := range []uint64{0, 1, 127, 128, 300, 1 << 63} {
		var w Writer
		w.Varint(1, v)
		fs, err := Parse(w.Out())
		if err != nil || len(fs) != 1 || fs[0].Num != 1 || fs[0].Val != v {
			t.Errorf("%d: got %+v err %v", v, fs, err)
		}
	}
	var w Writer
	w.Varint(1, 300)
	if !bytes.Equal(w.Out(), []byte{0x08, 0xac, 0x02}) {
		t.Errorf("300 encodes as % x", w.Out())
	}
}

func TestSintZigZag(t *testing.T) {
	var w Writer
	w.Sint(1, -1)
	w.Sint(2, 1)
	if !bytes.Equal(w.Out(), []byte{0x08, 0x01, 0x10, 0x02}) {
		t.Errorf("got % x", w.Out())
	}
}

func TestBytesAndString(t *testing.T) {
	var w Writer
	w.String(4, "hi")
	w.Bool(5, true)
	fs, err := Parse(w.Out())
	if err != nil || len(fs) != 2 || string(fs[0].Data) != "hi" || fs[0].Wire != 2 || fs[1].Val != 1 {
		t.Fatalf("got %+v err %v", fs, err)
	}
}

func TestParseTruncated(t *testing.T) {
	if _, err := Parse([]byte{0x0a, 0x05, 'a', 'b'}); err == nil {
		t.Error("length-delimited field longer than input: want error")
	}
	if _, err := Parse([]byte{0x08, 0x80}); err == nil {
		t.Error("unterminated varint: want error")
	}
}

func TestParseSkipsFixedWidth(t *testing.T) {
	// field 1 fixed64, field 2 fixed32, field 3 varint 7
	b := []byte{0x09, 1, 2, 3, 4, 5, 6, 7, 8, 0x15, 1, 2, 3, 4, 0x18, 0x07}
	fs, err := Parse(b)
	if err != nil || len(fs) != 3 || fs[2].Num != 3 || fs[2].Val != 7 {
		t.Fatalf("got %+v err %v", fs, err)
	}
}

func TestPackedVarints(t *testing.T) {
	got := PackedVarints([]byte{0x01, 0xac, 0x02, 0x03})
	if len(got) != 3 || got[0] != 1 || got[1] != 300 || got[2] != 3 {
		t.Fatalf("got %v", got)
	}
}
