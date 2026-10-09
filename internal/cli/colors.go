package cli

import (
	"bytes"
	"hash/fnv"
	"io"
	"regexp"
	"strings"
	"sync"
)

const (
	ansiReset        = "\x1b[0m"
	ansiPurple       = "\x1b[35m"
	ansiBlue         = "\x1b[34m"
	ansiBrightYellow = "\x1b[93m"
	ansiBrightRed    = "\x1b[91m"
)

var serviceColors = []string{
	"\x1b[36m", // cyan
	"\x1b[33m", // yellow
	"\x1b[32m", // green
	"\x1b[37m", // white
	"\x1b[31m", // red
	"\x1b[96m", // bright cyan
	"\x1b[93m", // bright yellow
}

// Upstream colors must not override ACC's event, progress, and service colors.
// Other terminal controls, including carriage returns, remain intact.
var ansiStylePattern = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

func newBuildLogWriter(target io.Writer, enabled bool) io.Writer {
	if !enabled {
		return target
	}
	return &colorWriter{target: target, color: ansiBlue}
}

func newPullLogWriter(target io.Writer, enabled bool) io.Writer {
	if !enabled {
		return target
	}
	return &colorWriter{target: target, color: ansiBlue}
}

func newServiceLogWriter(target io.Writer, enabled bool) io.Writer {
	if !enabled {
		return target
	}
	return &serviceLogWriter{target: target}
}

func writeWarning(target io.Writer, enabled bool, message string) error {
	line := "[Warning]: " + message + "\n"
	if enabled {
		line = ansiBrightYellow + line + ansiReset
	}
	_, err := io.WriteString(target, line)
	return err
}

func writeFatalWarning(target io.Writer, enabled bool, message string) error {
	line := "[Unsupported]: " + message + "\n"
	if enabled {
		line = ansiBrightRed + line + ansiReset
	}
	_, err := io.WriteString(target, line)
	return err
}

func writeReconcileLine(target io.Writer, message string) error {
	line := "[ACC] " + message + "\n"
	_, err := io.WriteString(target, line)
	return err
}

type synchronizedWriter struct {
	mu     sync.Mutex
	target io.Writer
}

func (w *synchronizedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.target.Write(p)
}

type colorWriter struct {
	target io.Writer
	color  string
}

func (w *colorWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	colored := make([]byte, 0, len(w.color)+len(p)+len(ansiReset))
	colored = append(colored, w.color...)
	colored = append(colored, ansiStylePattern.ReplaceAll(p, nil)...)
	colored = append(colored, ansiReset...)
	if _, err := w.target.Write(colored); err != nil {
		return 0, err
	}
	return len(p), nil
}

type serviceLogWriter struct {
	mu     sync.Mutex
	target io.Writer
	buf    []byte
	colors map[string]string
	used   map[string]bool
}

func (w *serviceLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.buf = append(w.buf, p...)
	for {
		lineEnd := bytes.IndexByte(w.buf, '\n')
		if lineEnd < 0 {
			return len(p), nil
		}

		line := w.buf[:lineEnd+1]
		w.buf = w.buf[lineEnd+1:]
		color := w.serviceColor(line)
		if color == "" {
			if _, err := w.target.Write(line); err != nil {
				return 0, err
			}
			continue
		}
		if _, err := io.WriteString(w.target, color+ansiStylePattern.ReplaceAllString(string(line), "")+ansiReset); err != nil {
			return 0, err
		}
	}
}

// serviceColor keeps each service's color stable for this writer and resolves
// hash collisions while unused palette entries remain. Write holds w.mu.
func (w *serviceLogWriter) serviceColor(line []byte) string {
	if len(line) < 3 || line[0] != '[' {
		return ""
	}
	end := bytes.IndexByte(line, ']')
	if end <= 1 || end+1 >= len(line) || line[end+1] != ' ' {
		return ""
	}
	if bytes.Equal(line[1:end], []byte("ACC")) {
		return ansiPurple
	}
	name := string(line[1:end])
	if color, exists := w.colors[name]; exists {
		return color
	}
	if w.colors == nil {
		w.colors = make(map[string]string)
		w.used = make(map[string]bool)
	}

	hash := fnv.New32a()
	_, _ = hash.Write([]byte(strings.ToLower(name)))
	start := int(hash.Sum32()) % len(serviceColors)
	color := serviceColors[start]
	for offset := 0; offset < len(serviceColors); offset++ {
		candidate := serviceColors[(start+offset)%len(serviceColors)]
		if !w.used[candidate] {
			color = candidate
			break
		}
	}
	w.colors[name] = color
	w.used[color] = true
	return color
}
