package cli

import (
	"bytes"
	"hash/fnv"
	"io"
	"strings"
	"sync"
)

const (
	ansiReset        = "\x1b[0m"
	ansiPurple       = "\x1b[35m"
	ansiBrightYellow = "\x1b[93m"
)

var serviceColors = []string{
	"\x1b[36m", // cyan
	"\x1b[33m", // yellow
	"\x1b[32m", // green
	"\x1b[34m", // blue
	"\x1b[31m", // red
	"\x1b[96m", // bright cyan
	"\x1b[93m", // bright yellow
}

func newBuildLogWriter(target io.Writer, enabled bool) io.Writer {
	if !enabled {
		return target
	}
	return &colorWriter{target: target, color: ansiPurple}
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

type colorWriter struct {
	target io.Writer
	color  string
}

func (w *colorWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if _, err := io.WriteString(w.target, w.color); err != nil {
		return 0, err
	}
	if _, err := w.target.Write(p); err != nil {
		return 0, err
	}
	if _, err := io.WriteString(w.target, ansiReset); err != nil {
		return 0, err
	}
	return len(p), nil
}

type serviceLogWriter struct {
	mu     sync.Mutex
	target io.Writer
	buf    []byte
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
		color := serviceColor(line)
		if color == "" {
			if _, err := w.target.Write(line); err != nil {
				return 0, err
			}
			continue
		}
		if _, err := io.WriteString(w.target, color+string(line)+ansiReset); err != nil {
			return 0, err
		}
	}
}

func serviceColor(line []byte) string {
	if len(line) < 3 || line[0] != '[' {
		return ""
	}
	end := bytes.IndexByte(line, ']')
	if end <= 1 || end+1 >= len(line) || line[end+1] != ' ' {
		return ""
	}

	hash := fnv.New32a()
	_, _ = hash.Write([]byte(strings.ToLower(string(line[1:end]))))
	return serviceColors[int(hash.Sum32())%len(serviceColors)]
}
