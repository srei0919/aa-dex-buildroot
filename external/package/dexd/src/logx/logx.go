// Package logx is dexd's log: a file that survives power-off (two files of
// at most 1 MB), standard error, and the last lines in memory for the status
// page. The Pi has no clock, so each line carries a boot counter and the
// seconds since boot instead of a date.
package logx

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxFile  = 1 << 20
	keepTail = 200
)

// Logger never fails: without a writable directory it logs to standard
// error and memory only.
type Logger struct {
	mu     sync.Mutex
	dir    string
	file   *os.File
	size   int64
	boot   int
	base   float64 // seconds since boot when Open was called
	opened time.Time
	tail   []string
	stderr io.Writer
	sync   func(*os.File) error
}

// Open starts logging into dir.
func Open(dir string) *Logger {
	l := &Logger{dir: dir, opened: time.Now(), base: uptime(), stderr: os.Stderr, sync: (*os.File).Sync}
	if os.MkdirAll(dir, 0o755) == nil {
		l.boot = nextBoot(filepath.Join(dir, "boot"))
		l.openFile()
	}
	return l
}

func nextBoot(path string) int {
	n := 0
	if b, err := os.ReadFile(path); err == nil {
		n, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	n++
	os.WriteFile(path, []byte(strconv.Itoa(n)+"\n"), 0o644)
	return n
}

func uptime() float64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

func (l *Logger) openFile() {
	f, err := os.OpenFile(filepath.Join(l.dir, "dexd.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	st, _ := f.Stat()
	l.file, l.size = f, 0
	if st != nil {
		l.size = st.Size()
	}
}

// Printf writes one line.
func (l *Logger) Printf(format string, args ...any) {
	l.line(fmt.Sprintf(format, args...))
}

func (l *Logger) line(msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	secs := l.base + time.Since(l.opened).Seconds()
	s := fmt.Sprintf("b%d %.3f %s", l.boot, secs, strings.TrimRight(msg, "\n"))
	if l.stderr != nil {
		fmt.Fprintln(l.stderr, s)
	}
	l.tail = append(l.tail, s)
	if len(l.tail) > 2*keepTail {
		l.tail = append([]string(nil), l.tail[len(l.tail)-keepTail:]...)
	}
	if l.file == nil {
		return
	}
	if l.size+int64(len(s)+1) > maxFile {
		l.file.Close()
		os.Rename(filepath.Join(l.dir, "dexd.log"), filepath.Join(l.dir, "dexd.log.1"))
		l.file = nil
		l.openFile()
		if l.file == nil {
			return
		}
	}
	if n, err := l.file.WriteString(s + "\n"); err == nil {
		l.size += int64(n)
		// Every test ends with the plug being pulled: get each line to disk.
		if l.sync != nil {
			l.sync(l.file)
		}
	}
}

// Write lets a log.Logger write through this one; each line is one entry.
func (l *Logger) Write(p []byte) (int, error) {
	for _, s := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		l.line(s)
	}
	return len(p), nil
}

// Std returns a standard library logger that writes here.
func (l *Logger) Std() *log.Logger { return log.New(l, "", 0) }

// Tail returns up to the last n lines.
func (l *Logger) Tail(n int) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n > len(l.tail) {
		n = len(l.tail)
	}
	return append([]string(nil), l.tail[len(l.tail)-n:]...)
}

// Close closes the file.
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
}

// Quiet stops copying lines to standard error (for tests).
func (l *Logger) Quiet() {
	l.mu.Lock()
	l.stderr = nil
	l.mu.Unlock()
}
