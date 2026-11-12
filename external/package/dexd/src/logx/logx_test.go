package logx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogRotation(t *testing.T) {
	dir := t.TempDir()
	l := Open(dir)
	line := strings.Repeat("x", 1000)
	for i := 0; i < 1200; i++ {
		l.Printf("%s", line)
	}
	l.Close()
	if _, err := os.Stat(filepath.Join(dir, "dexd.log.1")); err != nil {
		t.Fatalf("no rotated file: %v", err)
	}
	st, err := os.Stat(filepath.Join(dir, "dexd.log"))
	if err != nil || st.Size() >= 1<<20 {
		t.Fatalf("dexd.log: %v size %v", err, st)
	}
}

func TestLogUnwritableDir(t *testing.T) {
	file := filepath.Join(t.TempDir(), "plain")
	os.WriteFile(file, []byte("x"), 0o600)
	l := Open(filepath.Join(file, "sub"))
	l.Printf("still works %d", 7)
	got := l.Tail(5)
	if len(got) != 1 || !strings.HasSuffix(got[0], "still works 7") {
		t.Fatalf("tail %q", got)
	}
}

func TestBootCounter(t *testing.T) {
	dir := t.TempDir()
	a := Open(dir)
	a.Printf("one")
	a.Close()
	b := Open(dir)
	b.Printf("two")
	b.Close()
	if !strings.HasPrefix(a.Tail(1)[0], "b1 ") || !strings.HasPrefix(b.Tail(1)[0], "b2 ") {
		t.Fatalf("prefixes %q %q", a.Tail(1), b.Tail(1))
	}
	data, _ := os.ReadFile(filepath.Join(dir, "dexd.log"))
	if !strings.Contains(string(data), "one") || !strings.Contains(string(data), "two") {
		t.Fatalf("file %q", data)
	}
}

func TestTailKeepsLast(t *testing.T) {
	l := Open(t.TempDir())
	defer l.Close()
	l.Quiet()
	for i := 0; i < 300; i++ {
		l.Printf("line %d", i)
	}
	got := l.Tail(200)
	if len(got) != 200 || !strings.HasSuffix(got[199], "line 299") || !strings.HasSuffix(got[0], "line 100") {
		t.Fatalf("len %d first %q last %q", len(got), got[0], got[len(got)-1])
	}
}

func TestStdLoggerGoesThrough(t *testing.T) {
	l := Open(t.TempDir())
	defer l.Close()
	l.Quiet()
	l.Std().Printf("from std")
	if got := l.Tail(1); len(got) != 1 || !strings.HasSuffix(got[0], "from std") {
		t.Fatalf("tail %q", got)
	}
}
