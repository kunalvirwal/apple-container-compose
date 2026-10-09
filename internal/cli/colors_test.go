package cli

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"testing"
)

func TestServiceLogsHaveDistinctStableColors(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		t.Run(fmt.Sprintf("concurrent=%t", concurrent), func(t *testing.T) {
			var output bytes.Buffer
			writer := newServiceLogWriter(&output, true)
			names := []string{"backend", "default-client", "frontend", "relay"}
			var wg sync.WaitGroup
			for _, name := range names {
				write := func(name string) {
					for i := 0; i < 3; i++ {
						if _, err := fmt.Fprintf(writer, "[%s] ready\n", name); err != nil {
							t.Error(err)
						}
					}
				}
				if concurrent {
					wg.Add(1)
					go func(name string) { defer wg.Done(); write(name) }(name)
				} else {
					write(name)
				}
			}
			wg.Wait()
			pattern := regexp.MustCompile(`\x1b\[[0-9]+m\[([^]]+)\] ready\n\x1b\[0m`)
			matches := pattern.FindAllStringSubmatch(output.String(), -1)
			if len(matches) != len(names)*3 {
				t.Fatalf("missing or interleaved colored lines: %q", output.String())
			}
			colors := map[string]string{}
			owners := map[string]string{}
			for _, match := range matches {
				name := match[1]
				color := match[0][:strings.IndexByte(match[0], 'm')+1]
				if previous, ok := colors[name]; ok && previous != color {
					t.Fatalf("%s changed color from %q to %q", name, previous, color)
				}
				if owner, ok := owners[color]; ok && owner != name {
					t.Fatalf("%s and %s share color %q", owner, name, color)
				}
				if color == ansiPurple || color == ansiBlue || color == "\x1b[94m" || color == "\x1b[95m" {
					t.Fatalf("%s uses reserved progress/event color %q", name, color)
				}
				colors[name], owners[color] = color, name
			}
		})
	}
}

func TestServiceLogWriterPreservesChunkedLines(t *testing.T) {
	var output bytes.Buffer
	writer := newServiceLogWriter(&output, true)
	for _, chunk := range []string{"[back", "end] ready", "\nplain line\n[ACC] Created network\n"} {
		if _, err := io.WriteString(writer, chunk); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(output.String(), "[backend] ready\n"+ansiReset) ||
		!strings.Contains(output.String(), "plain line\n"+ansiPurple+"[ACC] Created network\n"+ansiReset) {
		t.Fatalf("output = %q", output.String())
	}
}

func TestServiceColorsStayStableAfterPaletteExhaustion(t *testing.T) {
	var output bytes.Buffer
	writer := newServiceLogWriter(&output, true)
	for pass := 0; pass < 2; pass++ {
		for i := 0; i < len(serviceColors)+2; i++ {
			if _, err := fmt.Fprintf(writer, "[service-%d] ready\n", i); err != nil {
				t.Fatal(err)
			}
		}
	}
	got := output.Bytes()
	if !bytes.Equal(got[:len(got)/2], got[len(got)/2:]) {
		t.Fatalf("colors changed after exhausting the palette: %q", got)
	}
}

func TestUpstreamStylesCannotOverrideAssignedColors(t *testing.T) {
	for _, test := range []struct {
		name      string
		newWriter func(io.Writer, bool) io.Writer
		chunks    []string
		plain     string
	}{
		{"build", newBuildLogWriter, []string{"\x1b[32mbuilding\x1b[0m image\r"}, "building image\r"},
		{"pull", newPullLogWriter, []string{"\x1b[35mfetching\x1b[0m image\r"}, "fetching image\r"},
		{"service", newServiceLogWriter, []string{"[backend] \x1b[3", "5mready\x1b[0m\n"}, "[backend] ready\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			writer := test.newWriter(&output, true)
			for _, chunk := range test.chunks {
				n, err := io.WriteString(writer, chunk)
				if err != nil || n != len(chunk) {
					t.Fatalf("Write() = %d, %v", n, err)
				}
			}
			var expected bytes.Buffer
			if _, err := io.WriteString(test.newWriter(&expected, true), test.plain); err != nil {
				t.Fatal(err)
			}
			if output.String() != expected.String() {
				t.Fatalf("output = %q, want %q", output.String(), expected.String())
			}
		})
	}
}

func TestLogWritersRespectColorOption(t *testing.T) {
	for _, test := range []struct {
		name           string
		newWriter      func(io.Writer, bool) io.Writer
		message, color string
	}{
		{"build", newBuildLogWriter, "building image\r", ansiBlue},
		{"pull", newPullLogWriter, "fetching image\r", ansiBlue},
		{"service", newServiceLogWriter, "[backend] ready\n", ""},
	} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/color=%t", test.name, enabled), func(t *testing.T) {
				var output bytes.Buffer
				n, err := io.WriteString(test.newWriter(&output, enabled), test.message)
				if err != nil || n != len(test.message) {
					t.Fatalf("Write() = %d, %v", n, err)
				}
				if !enabled && output.String() != test.message {
					t.Fatalf("plain output = %q", output.String())
				}
				if enabled && !strings.HasSuffix(output.String(), test.message+ansiReset) {
					t.Fatalf("colored output = %q", output.String())
				}
				if enabled && test.color != "" && output.String() != test.color+test.message+ansiReset {
					t.Fatalf("progress output = %q", output.String())
				}
			})
		}
	}
}
