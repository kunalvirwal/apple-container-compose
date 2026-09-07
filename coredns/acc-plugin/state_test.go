package accplugin

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStateValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*State)
	}{
		{"version", func(s *State) { s.Version = 2 }},
		{"missing containers", func(s *State) { s.Containers = nil }},
		{"blank id", func(s *State) { s.Containers[0].ID = "" }},
		{"duplicate id", func(s *State) { s.Containers[1].ID = s.Containers[0].ID }},
		{"missing network", func(s *State) { s.Containers[0].Networks = nil }},
		{"invalid network", func(s *State) {
			s.Containers[0].Networks = map[string]NetworkAttachment{" ": {Addresses: []string{"10.0.0.2"}}}
		}},
		{"no address", func(s *State) { s.Containers[0].Networks["db"] = NetworkAttachment{} }},
		{"duplicate source", func(s *State) { s.Containers[1].Networks["db"] = NetworkAttachment{Addresses: []string{"10.10.0.2"}} }},
		{"bad alias", func(s *State) {
			s.Containers[0].Networks["db"] = NetworkAttachment{Addresses: []string{"10.10.0.2"}, Aliases: []string{"bad..alias"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixtureState()
			tc.mutate(&s)
			if _, err := buildSnapshot(s); err == nil {
				t.Fatal("accepted invalid state")
			}
		})
	}
	for _, name := range []string{"", ".", "bad..name", "-api", "api-", " api", "api ", "a b", "*.api", "a/b", "a\\b", "café", strings.Repeat("a", 64), strings.Repeat("a.", 127) + "a"} {
		t.Run("name "+name, func(t *testing.T) {
			if _, err := normalizeName(name); err == nil {
				t.Fatalf("accepted %q", name)
			}
		})
	}
	for _, address := range []string{"", "garbage", "10.0.0.2/24", "0.0.0.0", "::", "127.0.0.1", "::1", "224.0.0.1", "ff02::1", "fe80::1", "fd00::1%eth0", "169.254.1.2", "255.255.255.255"} {
		t.Run("IP "+address, func(t *testing.T) {
			if _, err := attachmentAddresses(NetworkAttachment{Addresses: []string{address}}); err == nil {
				t.Fatalf("accepted %q", address)
			}
		})
	}
	for _, raw := range []string{
		"null", "{}", "{", "[]", `{"version":1,"containers":null}`,
		`{"version":1,"containers":[],"generation":1}`,
		`{"version":1,"containers":[]} {}`,
		`{"version":1,"version":2,"containers":[]}`,
		`{"version":1,"containers":[{"id":"a","project":"p","service":"a","networks":{"n":{"addresses":["10.0.0.1"],"ipv4":"10.0.0.1"}}}]}`,
	} {
		if _, err := decodeSnapshot([]byte(raw)); err == nil {
			t.Fatalf("accepted invalid JSON/schema %s", raw)
		}
	}
	for _, name := range []string{"API.", "api_service", "api.internal"} {
		if _, err := normalizeName(name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := decodeSnapshot([]byte(`{"version":1,"containers":[]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := attachmentAddresses(NetworkAttachment{Addresses: []string{"10.0.0.2", "fd00::2", "::ffff:10.0.0.2"}}); err != nil {
		t.Fatal(err)
	}
}

func TestReloadDetectionAndRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	original := marshalState(t, fixtureState())
	atomicWrite(t, path, original)
	r, err := NewFileRegistry(path, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var logs []string
	r.logError = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	var infoLogs []string
	r.logInfo = func(format string, args ...any) { infoLogs = append(infoLogs, fmt.Sprintf(format, args...)) }
	initial := r.current.Load()
	lastModified := r.lastModified
	if err := r.Reload(); err != nil {
		t.Fatal(err)
	}
	if r.lastModified != lastModified || r.current.Load() != initial {
		t.Fatal("unchanged modification time did not skip revalidation")
	}
	if err := r.Reload(); err != nil {
		t.Fatal(err)
	}
	if r.current.Load() != initial {
		t.Fatal("unchanged content rebuilt snapshot")
	}
	// A newer replacement is revalidated even when its contents are identical.
	atomicWrite(t, path, original)
	if err := r.Reload(); err != nil {
		t.Fatal(err)
	}
	if r.current.Load() == initial {
		t.Fatal("new modification time did not rebuild snapshot")
	}
	if len(infoLogs) != 1 || !strings.Contains(infoLogs[0], "state updated") {
		t.Fatalf("successful replacement was not logged: %v", infoLogs)
	}
	initial = r.current.Load()
	for _, bad := range []string{"{", `{"version":1,"containers":null}`} {
		atomicWrite(t, path, []byte(bad))
		if err := r.Reload(); err == nil {
			t.Fatal("missing validation failure")
		}
		if r.current.Load() != initial {
			t.Fatal("invalid update replaced live state")
		}
		if err := r.Reload(); err != nil {
			t.Fatal("unchanged invalid state was reparsed")
		}
	}
	if len(logs) != 2 {
		t.Fatalf("expected two distinct errors with duplicates suppressed, got %v", logs)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := r.Reload(); err == nil || r.current.Load() != initial {
		t.Fatal("missing file must retain state")
	}
	changed := fixtureState()
	changed.Containers[0].Networks["db"] = NetworkAttachment{Addresses: []string{"10.10.0.9"}}
	atomicWrite(t, path, marshalState(t, changed))
	if err := r.Reload(); err != nil {
		t.Fatal(err)
	}
	result := r.Resolve(netip.MustParseAddr("10.20.0.3"), "db")
	if result.Addresses[0].String() != "10.10.0.9" {
		t.Fatal("valid update not installed")
	}
	// Explicitly empty state is a valid removal of all registrations.
	atomicWrite(t, path, []byte(`{"version":1,"containers":[]}`))
	if err := r.Reload(); err != nil {
		t.Fatal(err)
	}
	if r.Resolve(netip.MustParseAddr("10.20.0.3"), "db").Kind != ResolutionUnmanaged {
		t.Fatal("stopped services retained")
	}
}

func TestPollingAndConcurrentQueries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	atomicWrite(t, path, marshalState(t, fixtureState()))
	r, err := NewFileRegistry(path, 2*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var logCount atomic.Int32
	r.logError = func(string, ...any) { logCount.Add(1) }
	r.Start()
	r.Start()
	initial := r.current.Load()
	atomicWrite(t, path, []byte("{"))
	await(t, func() bool { return logCount.Load() > 0 })
	if r.current.Load() != initial {
		t.Fatal("poll published invalid state")
	}
	var queries sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		queries.Add(1)
		go func() {
			defer queries.Done()
			for {
				select {
				case <-stop:
					return
				default:
					r.Resolve(netip.MustParseAddr("10.20.0.3"), "db")
				}
			}
		}()
	}
	defer func() { close(stop); queries.Wait() }()
	updated := fixtureState()
	updated.Containers[0].Networks["db"] = NetworkAttachment{Addresses: []string{"10.10.0.9"}}
	atomicWrite(t, path, marshalState(t, updated))
	await(t, func() bool { return r.current.Load() != initial })
	r.Close()
	r.Close()
	r.Start()
	select {
	case <-r.finished:
	default:
		t.Fatal("Close did not join polling goroutine")
	}
	final := r.current.Load()
	atomicWrite(t, path, marshalState(t, fixtureState()))
	time.Sleep(10 * time.Millisecond)
	if r.current.Load() != final {
		t.Fatal("closed registry restarted")
	}
}

func TestRegistryStartupValidation(t *testing.T) {
	for _, tc := range []struct {
		path     string
		interval time.Duration
	}{
		{"relative.json", time.Second}, {"/unused", 0},
		{filepath.Join(t.TempDir(), "missing"), time.Second}, {t.TempDir(), time.Second},
	} {
		if _, err := NewFileRegistry(tc.path, tc.interval); err == nil {
			t.Fatal("invalid startup accepted")
		}
	}
	path := filepath.Join(t.TempDir(), "state.json")
	atomicWrite(t, path, []byte(`{"version":1,"containers":[]}`))
	r, err := NewFileRegistry(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	r.Start()
	r.Close()
	if r.started {
		t.Fatal("started after Close")
	}
}

func marshalState(t *testing.T, s State) []byte {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func atomicWrite(t *testing.T, path string, contents []byte) {
	t.Helper()
	file, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(contents); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(file.Name(), path); err != nil {
		t.Fatal(err)
	}
	modified := time.Now().Add(time.Second)
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
}
func await(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for poll")
		}
		time.Sleep(time.Millisecond)
	}
}
