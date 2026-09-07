package accplugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coredns/coredns/plugin/pkg/log"
)

var registryLog = log.NewWithPlugin("acc")

// FileRegistry serves immutable DNS indexes loaded from a mounted state file.
type FileRegistry struct {
	path     string
	interval time.Duration
	current  atomic.Pointer[snapshot]

	reloadMu     sync.Mutex
	lastModified time.Time
	logError     func(string, ...any)
	logInfo      func(string, ...any)

	lifecycleMu sync.Mutex
	stop        chan struct{}
	finished    chan struct{}
	started     bool
	closed      bool
}

// NewFileRegistry validates and loads the initial state. A missing or invalid
// initial file prevents CoreDNS from starting because no registry is available.
func NewFileRegistry(path string, interval time.Duration) (*FileRegistry, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("state file path must be absolute")
	}
	if interval <= 0 {
		return nil, fmt.Errorf("poll interval must be positive")
	}

	r := &FileRegistry{
		path:     path,
		interval: interval,
		stop:     make(chan struct{}),
		finished: make(chan struct{}),
		logError: registryLog.Errorf,
		logInfo:  registryLog.Infof,
	}
	if err := r.Reload(); err != nil {
		return nil, err
	}
	return r, nil
}

// Start polls state.json's modification time. Mount its parent directory rather
// than state.json alone: ACC replaces the file with an atomic rename.
func (r *FileRegistry) Start() {
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	if r.started || r.closed {
		return
	}
	r.started = true

	go func() {
		defer close(r.finished)
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			select {
			case <-r.stop:
				return
			case <-ticker.C:
				_ = r.Reload()
			}
		}
	}()
}

// Close stops the polling goroutine. A closed registry continues to serve its
// last snapshot and cannot be restarted.
func (r *FileRegistry) Close() {
	r.lifecycleMu.Lock()
	if !r.closed {
		r.closed = true
		close(r.stop)
	}
	started := r.started
	r.lifecycleMu.Unlock()

	if started {
		<-r.finished
	}
}

// Reload reads and validates state.json only after its modification time
// changes. Invalid replacements are logged and leave the live registry intact.
func (r *FileRegistry) Reload() error {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()

	info, err := os.Stat(r.path)
	if err != nil {
		return r.reloadError("stat state file %q: %w", r.path, err)
	}
	if !info.Mode().IsRegular() {
		return r.reloadError("state file %q must be a regular file", r.path)
	}
	if !r.lastModified.IsZero() && info.ModTime().Equal(r.lastModified) {
		return nil
	}

	contents, err := os.ReadFile(r.path)
	if err != nil {
		return r.reloadError("read state file %q: %w", r.path, err)
	}

	// Record this version before validation so an unchanged invalid file is not
	// reparsed every polling interval. ACC publishes every retry as a new file.
	r.lastModified = info.ModTime()
	candidate, err := decodeSnapshot(contents)
	if err != nil {
		return r.reloadError("load state file: %w", err)
	}
	message := "state updated"
	if r.current.Load() == nil {
		message = "initial state loaded"
	}
	r.current.Store(candidate)
	r.logInfo("%s from %q", message, r.path)
	return nil
}

func (r *FileRegistry) reloadError(format string, args ...any) error {
	err := fmt.Errorf(format, args...)
	r.logError("state reload failed; retaining current registry: %v", err)
	return err
}

func decodeSnapshot(contents []byte) (*snapshot, error) {
	var state State
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return nil, fmt.Errorf("decode state: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("state must contain exactly one JSON value")
	}
	return buildSnapshot(state)
}
