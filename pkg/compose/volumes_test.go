package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"

	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

func TestUpVolumeMountTypes(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		assert   func(t *testing.T, projectDir string, calls [][]string)
	}{
		{
			name: "bind",
			contents: `services:
  app:
    image: alpine
    volumes:
      - type: bind
        source: ./host-data
        target: /data
        read_only: true
`,
			assert: func(t *testing.T, projectDir string, calls [][]string) {
				t.Helper()
				source := filepath.Join(projectDir, "host-data")
				if info, err := os.Stat(source); err != nil || !info.IsDir() {
					t.Fatalf("bind source = %v, err = %v; want created directory", info, err)
				}
				if len(calls) != 1 {
					t.Fatalf("calls = %#v, want one container run", calls)
				}
				wantMount := "type=bind,source=" + source + ",target=/data,readonly"
				if !hasArgument(calls[0], wantMount) {
					t.Fatalf("run args = %#v, want mount %q", calls[0], wantMount)
				}
			},
		},
		{
			name: "named project scoped",
			contents: `name: demo
services:
  app:
    image: alpine
    volumes:
      - type: volume
        source: pgdata
        target: /var/lib/data
        volume:
          labels:
            com.example.mount: service
volumes:
  pgdata:
    labels:
      com.example.team: platform
`,
			assert: func(t *testing.T, _ string, calls [][]string) {
				t.Helper()
				if len(calls) != 3 {
					t.Fatalf("calls = %#v, want inspect, create, run", calls)
				}
				if !reflect.DeepEqual(calls[0], []string{"volume", "inspect", "demo_pgdata"}) {
					t.Fatalf("inspect args = %#v", calls[0])
				}
				wantCreate := []string{
					"volume", "create",
					"--label", "com.example.mount=service",
					"--label", "com.example.team=platform",
					"--label", accProjectLabel + "=demo",
					"--label", accVolumeLabel + "=pgdata",
					"demo_pgdata",
				}
				if !reflect.DeepEqual(calls[1], wantCreate) {
					t.Fatalf("create args = %#v, want %#v", calls[1], wantCreate)
				}
				if !hasArgument(calls[2], "type=volume,source=demo_pgdata,target=/var/lib/data") {
					t.Fatalf("run args = %#v", calls[2])
				}
			},
		},
		{
			name: "named explicit name",
			contents: `name: demo
services:
  app:
    image: alpine
    volumes:
      - pgdata:/var/lib/data
volumes:
  pgdata:
    name: production-db
`,
			assert: func(t *testing.T, _ string, calls [][]string) {
				t.Helper()
				if len(calls) != 3 {
					t.Fatalf("calls = %#v, want inspect, create, run", calls)
				}
				if !reflect.DeepEqual(calls[0], []string{"volume", "inspect", "production-db"}) {
					t.Fatalf("inspect args = %#v", calls[0])
				}
				if !hasArgument(calls[1], "production-db") || !hasArgument(calls[2], "type=volume,source=production-db,target=/var/lib/data") {
					t.Fatalf("calls = %#v", calls)
				}
			},
		},
		{
			name: "anonymous",
			contents: `name: demo
services:
  app:
    image: alpine
    volumes:
      - /cache
`,
			assert: func(t *testing.T, _ string, calls [][]string) {
				t.Helper()
				if len(calls) != 3 || !reflect.DeepEqual(calls[0], []string{"list", "--format", "json", "--all"}) {
					t.Fatalf("calls = %#v, want container list, volume create, run", calls)
				}
				created := calls[1]
				if len(created) != 7 || !reflect.DeepEqual(created[:4], []string{"volume", "create", "--label", accProjectLabel + "=demo"}) {
					t.Fatalf("create args = %#v", created)
				}
				volumeName := created[6]
				if !regexp.MustCompile(`^acc-demo-anon-[a-f0-9]{32}$`).MatchString(volumeName) {
					t.Fatalf("anonymous volume name = %q", volumeName)
				}
				if created[5] != accVolumeLabel+"="+volumeName {
					t.Fatalf("anonymous volume label = %q", created[5])
				}
				if !hasArgument(calls[2], "type=volume,source="+volumeName+",target=/cache") {
					t.Fatalf("run args = %#v", calls[2])
				}
			},
		},
		{
			name: "tmpfs",
			contents: `services:
  app:
    image: alpine
    volumes:
      - type: tmpfs
        target: /cache
        tmpfs:
          size: 512M
          mode: 1777
`,
			assert: func(t *testing.T, _ string, calls [][]string) {
				t.Helper()
				if len(calls) != 1 {
					t.Fatalf("calls = %#v, want one container run", calls)
				}
				if !hasArgument(calls[0], "type=tmpfs,target=/cache,size=536870912,mode=1777") {
					t.Fatalf("run args = %#v, want tmpfs mount", calls[0])
				}
			},
		},
		{
			name: "tmpfs shorthand",
			contents: `services:
  app:
    image: alpine
    tmpfs:
      - /cache:ro,size=512M,mode=1777
`,
			assert: func(t *testing.T, _ string, calls [][]string) {
				t.Helper()
				if len(calls) != 1 {
					t.Fatalf("calls = %#v, want one container run", calls)
				}
				if !hasArgument(calls[0], "type=tmpfs,target=/cache,size=536870912,mode=1777,readonly") {
					t.Fatalf("run args = %#v, want tmpfs mount", calls[0])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectDir, composePath := writeVolumeCompose(t, tt.contents)
			var calls [][]string
			run := func(_ context.Context, args ...string) (string, error) {
				calls = append(calls, append([]string(nil), args...))
				switch {
				case len(args) >= 2 && args[0] == "volume" && args[1] == "inspect":
					return "volume not found", errors.New("command failed")
				case reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}):
					return "[]", nil
				default:
					return "", nil
				}
			}
			client := newVolumeTestClient(run)

			if err := client.Up(context.Background(), composePath, ParseOptions{}, UpOptions{}); err != nil {
				t.Fatalf("Up() error = %v", err)
			}
			tt.assert(t, projectDir, calls)
		})
	}
}

func TestUpAppliesAnonymousServiceVolumeLabels(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `name: demo
services:
  app:
    image: alpine
    volumes:
      - type: volume
        target: /cache
        volume:
          labels:
            com.example.scope: anonymous
`)
	var calls [][]string
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		if reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}) {
			return "[]", nil
		}
		return "", nil
	}
	if err := newVolumeTestClient(run).Up(context.Background(), composePath, ParseOptions{}, UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if len(calls) != 3 {
		t.Fatalf("calls = %#v, want container list, volume create, run", calls)
	}
	created := calls[1]
	if len(created) != 9 || !reflect.DeepEqual(created[:6], []string{
		"volume", "create",
		"--label", "com.example.scope=anonymous",
		"--label", accProjectLabel + "=demo",
	}) {
		t.Fatalf("create args = %#v", created)
	}
	volumeName := created[8]
	if created[7] != accVolumeLabel+"="+volumeName {
		t.Fatalf("anonymous volume label = %q", created[7])
	}
}

func TestUpWarnsForIgnoredVolumeConsistency(t *testing.T) {
	tests := []struct {
		name        string
		contents    string
		consistency string
	}{
		{
			name: "long syntax",
			contents: `services:
  app:
    image: alpine
    volumes:
      - type: bind
        source: .
        target: /app
        consistency: delegated
`,
			consistency: "delegated",
		},
		{
			name: "short syntax",
			contents: `services:
  app:
    image: alpine
    volumes:
      - .:/app:cached
`,
			consistency: "cached",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, composePath := writeVolumeCompose(t, tt.contents)
			var warnings []string
			err := newVolumeTestClient(func(_ context.Context, _ ...string) (string, error) {
				return "", nil
			}).Up(context.Background(), composePath, ParseOptions{}, UpOptions{
				OnWarning: func(message string) { warnings = append(warnings, message) },
			})
			if err != nil {
				t.Fatalf("Up() error = %v", err)
			}
			want := []string{`service "app" volume consistency "` + tt.consistency + `" is not supported by ACC and will be ignored`}
			if !reflect.DeepEqual(warnings, want) {
				t.Fatalf("warnings = %#v, want %#v", warnings, want)
			}
		})
	}
}

func TestUpReportsFatalWarningForUnknownShortSyntaxVolumeOption(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `services:
  app:
    image: alpine
    volumes:
      - data:/app/data:agjfhs
volumes:
  data:
`)
	var warnings []string
	err := newVolumeTestClient(func(_ context.Context, args ...string) (string, error) {
		t.Fatalf("unexpected runtime call: %#v", args)
		return "", nil
	}).Up(context.Background(), composePath, ParseOptions{}, UpOptions{
		OnFatalWarning: func(message string) { warnings = append(warnings, message) },
	})
	if !errors.Is(err, ErrUnsupportedFeature) {
		t.Fatalf("Up() error = %v, want ErrUnsupportedFeature", err)
	}
	want := []string{`service "app" volume option "agjfhs" is not supported by ACC`}
	if !reflect.DeepEqual(warnings, want) {
		t.Fatalf("fatal warnings = %#v, want %#v", warnings, want)
	}
}

func TestUpReportsFatalWarningForBindPropagation(t *testing.T) {
	tests := []struct {
		name     string
		contents string
	}{
		{
			name: "long syntax",
			contents: `services:
  app:
    image: alpine
    volumes:
      - type: bind
        source: .
        target: /app
        bind:
          propagation: rshared
`,
		},
		{
			name: "short syntax",
			contents: `services:
  app:
    image: alpine
    volumes:
      - .:/app:rshared
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, composePath := writeVolumeCompose(t, tt.contents)
			var warnings []string
			err := newVolumeTestClient(func(_ context.Context, args ...string) (string, error) {
				t.Fatalf("unexpected runtime call: %#v", args)
				return "", nil
			}).Up(context.Background(), composePath, ParseOptions{}, UpOptions{
				OnFatalWarning: func(message string) { warnings = append(warnings, message) },
			})
			if !errors.Is(err, ErrUnsupportedFeature) {
				t.Fatalf("Up() error = %v, want ErrUnsupportedFeature", err)
			}
			want := []string{`service "app" bind.propagation "rshared" is not supported by ACC`}
			if !reflect.DeepEqual(warnings, want) {
				t.Fatalf("fatal warnings = %#v, want %#v", warnings, want)
			}
		})
	}
}

func TestUpWarnsForIgnoredBindSELinux(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		selinux  string
	}{
		{
			name: "long syntax",
			contents: `services:
  app:
    image: alpine
    volumes:
      - type: bind
        source: .
        target: /app
        bind:
          selinux: z
`,
			selinux: "z",
		},
		{
			name: "short syntax",
			contents: `services:
  app:
    image: alpine
    volumes:
      - .:/app:Z
`,
			selinux: "Z",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, composePath := writeVolumeCompose(t, tt.contents)
			var warnings []string
			err := newVolumeTestClient(func(_ context.Context, _ ...string) (string, error) {
				return "", nil
			}).Up(context.Background(), composePath, ParseOptions{}, UpOptions{
				OnWarning: func(message string) { warnings = append(warnings, message) },
			})
			if err != nil {
				t.Fatalf("Up() error = %v", err)
			}
			want := []string{`service "app" bind.selinux "` + tt.selinux + `" is not supported by ACC and will be ignored`}
			if !reflect.DeepEqual(warnings, want) {
				t.Fatalf("warnings = %#v, want %#v", warnings, want)
			}
		})
	}
}

func TestUpReportsFatalWarningsForUnsupportedServiceVolumeOptions(t *testing.T) {
	tests := []struct {
		name    string
		option  string
		warning string
	}{
		{
			name:    "nocopy",
			option:  "nocopy: true",
			warning: `service "app" volume.nocopy is not supported by ACC`,
		},
		{
			name:    "subpath",
			option:  "subpath: nested",
			warning: `service "app" volume.subpath is not supported by ACC`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, composePath := writeVolumeCompose(t, `services:
  app:
    image: alpine
    volumes:
      - type: volume
        source: data
        target: /data
        volume:
          `+tt.option+`
volumes:
  data:
`)
			var warnings []string
			err := newVolumeTestClient(func(_ context.Context, args ...string) (string, error) {
				t.Fatalf("unexpected runtime call: %#v", args)
				return "", nil
			}).Up(context.Background(), composePath, ParseOptions{}, UpOptions{
				OnFatalWarning: func(message string) { warnings = append(warnings, message) },
			})
			if !errors.Is(err, ErrUnsupportedFeature) {
				t.Fatalf("Up() error = %v, want ErrUnsupportedFeature", err)
			}
			if want := []string{tt.warning}; !reflect.DeepEqual(warnings, want) {
				t.Fatalf("fatal warnings = %#v, want %#v", warnings, want)
			}
		})
	}
}

func TestUpReportsFatalWarningForReservedServiceVolumeLabel(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `services:
  app:
    image: alpine
    volumes:
      - type: volume
        source: data
        target: /data
        volume:
          labels:
            io.github.kunalvirwal.acc.volume: user-defined
volumes:
  data:
`)
	var warnings []string
	err := newVolumeTestClient(func(_ context.Context, args ...string) (string, error) {
		t.Fatalf("unexpected runtime call: %#v", args)
		return "", nil
	}).Up(context.Background(), composePath, ParseOptions{}, UpOptions{
		OnFatalWarning: func(message string) { warnings = append(warnings, message) },
	})
	if !errors.Is(err, ErrUnsupportedFeature) {
		t.Fatalf("Up() error = %v, want ErrUnsupportedFeature", err)
	}
	want := []string{`service "app" volume label "io.github.kunalvirwal.acc.volume" is reserved for ACC`}
	if !reflect.DeepEqual(warnings, want) {
		t.Fatalf("fatal warnings = %#v, want %#v", warnings, want)
	}
}

func TestUpReportsFatalWarningForReservedVolumeLabel(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `services:
  app:
    image: alpine
    volumes:
      - data:/data
volumes:
  data:
    labels:
      io.github.kunalvirwal.acc.project: user-defined
`)
	var warnings []string
	err := newVolumeTestClient(func(_ context.Context, args ...string) (string, error) {
		t.Fatalf("unexpected runtime call: %#v", args)
		return "", nil
	}).Up(context.Background(), composePath, ParseOptions{}, UpOptions{
		OnFatalWarning: func(message string) {
			warnings = append(warnings, message)
		},
	})
	if !errors.Is(err, ErrUnsupportedFeature) {
		t.Fatalf("Up() error = %v, want ErrUnsupportedFeature", err)
	}
	want := []string{`volume "data" label "io.github.kunalvirwal.acc.project" is reserved for ACC`}
	if !reflect.DeepEqual(warnings, want) {
		t.Fatalf("fatal warnings = %#v, want %#v", warnings, want)
	}
}

func TestUpReportsFatalWarningForUnsupportedVolumeDriver(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `services:
  app:
    image: alpine
    volumes:
      - data:/data
volumes:
  data:
    driver: nfs
`)
	var warnings []string
	err := newVolumeTestClient(func(_ context.Context, args ...string) (string, error) {
		t.Fatalf("unexpected runtime call: %#v", args)
		return "", nil
	}).Up(context.Background(), composePath, ParseOptions{}, UpOptions{
		OnFatalWarning: func(message string) {
			warnings = append(warnings, message)
		},
	})
	if !errors.Is(err, ErrUnsupportedFeature) {
		t.Fatalf("Up() error = %v, want ErrUnsupportedFeature", err)
	}
	want := []string{`volume "data" driver "nfs" is not supported by ACC; only the local driver is supported`}
	if !reflect.DeepEqual(warnings, want) {
		t.Fatalf("fatal warnings = %#v, want %#v", warnings, want)
	}
}

func TestUpValidatesVolumeDriverOptions(t *testing.T) {
	tests := []struct {
		name    string
		driver  string
		warning string
	}{
		{
			name:    "unsupported option",
			driver:  "compression: fast",
			warning: `volume "data" driver_opts.compression is not supported by ACC`,
		},
		{
			name:    "size below Apple minimum",
			driver:  "size: 64K",
			warning: `volume "data" driver_opts.size "64K" is invalid: must be at least 1MiB with an optional K, M, G, T, or P suffix`,
		},
		{
			name:    "invalid journal mode",
			driver:  "journal: ordersded:64M",
			warning: `volume "data" driver_opts.journal "ordersded:64M" is invalid: mode must be writeback, ordered, or journal`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, composePath := writeVolumeCompose(t, `services:
  app:
    image: alpine
    volumes:
      - data:/data
volumes:
  data:
    driver_opts:
      `+tt.driver+`
`)
			var warnings []string
			err := newVolumeTestClient(func(_ context.Context, args ...string) (string, error) {
				t.Fatalf("unexpected runtime call: %#v", args)
				return "", nil
			}).Up(context.Background(), composePath, ParseOptions{}, UpOptions{
				OnFatalWarning: func(message string) {
					warnings = append(warnings, message)
				},
			})
			if !errors.Is(err, ErrUnsupportedFeature) {
				t.Fatalf("Up() error = %v, want ErrUnsupportedFeature", err)
			}
			if want := []string{tt.warning}; !reflect.DeepEqual(warnings, want) {
				t.Fatalf("fatal warnings = %#v, want %#v", warnings, want)
			}
		})
	}
}

func TestUpPassesValidVolumeDriverOptions(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `name: demo
services:
  app:
    image: alpine
    volumes:
      - data:/data
volumes:
  data:
    driver_opts:
      size: 30M
      journal: ordered:64M
`)
	var calls [][]string
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(args) >= 2 && args[0] == "volume" && args[1] == "inspect" {
			return "volume not found", errors.New("command failed")
		}
		return "", nil
	}
	if err := newVolumeTestClient(run).Up(context.Background(), composePath, ParseOptions{}, UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	wantCreate := []string{
		"volume", "create",
		"--label", accProjectLabel + "=demo",
		"--label", accVolumeLabel + "=data",
		"--opt", "journal=ordered:64M",
		"--opt", "size=30M",
		"demo_data",
	}
	if !hasCall(calls, wantCreate) {
		t.Fatalf("Up() calls = %#v, want %#v", calls, wantCreate)
	}
}

func TestUpReportsFatalWarningForVolumesFrom(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `services:
  app:
    image: alpine
    volumes_from:
      - database
  database:
    image: alpine
`)
	var warnings []string
	err := newVolumeTestClient(func(_ context.Context, args ...string) (string, error) {
		t.Fatalf("unexpected runtime call: %#v", args)
		return "", nil
	}).Up(context.Background(), composePath, ParseOptions{}, UpOptions{
		OnFatalWarning: func(message string) {
			warnings = append(warnings, message)
		},
	})
	if !errors.Is(err, ErrUnsupportedFeature) {
		t.Fatalf("Up() error = %v, want ErrUnsupportedFeature", err)
	}
	want := []string{`service "app" volumes_from is not supported by ACC, please define volume mounts separately`}
	if !reflect.DeepEqual(warnings, want) {
		t.Fatalf("fatal warnings = %#v, want %#v", warnings, want)
	}
}

func TestUpReportsFatalWarningForUnsupportedVolumeType(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `services:
  app:
    image: alpine
    volumes:
      - type: image
        source: alpine
        target: /source
`)
	var warnings []string
	err := newVolumeTestClient(func(_ context.Context, args ...string) (string, error) {
		t.Fatalf("unexpected runtime call: %#v", args)
		return "", nil
	}).Up(context.Background(), composePath, ParseOptions{}, UpOptions{
		OnFatalWarning: func(message string) {
			warnings = append(warnings, message)
		},
	})
	if !errors.Is(err, ErrUnsupportedFeature) {
		t.Fatalf("Up() error = %v, want ErrUnsupportedFeature", err)
	}
	want := []string{`service "app" volume type "image" is not supported by ACC`}
	if !reflect.DeepEqual(warnings, want) {
		t.Fatalf("fatal warnings = %#v, want %#v", warnings, want)
	}
}

func TestUpRejectsSharedWritableNamedVolume(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `services:
  reader:
    image: alpine
    volumes:
      - data:/data:ro
  writer:
    image: alpine
    volumes:
      - data:/data
volumes:
  data:
`)
	var warnings []string
	err := newVolumeTestClient(func(_ context.Context, args ...string) (string, error) {
		t.Fatalf("unexpected runtime call: %#v", args)
		return "", nil
	}).Up(context.Background(), composePath, ParseOptions{}, UpOptions{
		OnFatalWarning: func(message string) {
			warnings = append(warnings, message)
		},
	})
	if !errors.Is(err, ErrUnsupportedFeature) {
		t.Fatalf("Up() error = %v, want ErrUnsupportedFeature", err)
	}
	want := []string{`named volume "data" is mounted by multiple services, but Apple Container only supports read-only shared named volumes; use a bind mount for shared writable storage`}
	if !reflect.DeepEqual(warnings, want) {
		t.Fatalf("fatal warnings = %#v, want %#v", warnings, want)
	}
}

func TestUpAllowsSharedReadOnlyNamedVolume(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `name: demo
services:
  app:
    image: alpine
    volumes:
      - data:/app-data:ro
  worker:
    image: alpine
    volumes:
      - data:/worker-data:ro
volumes:
  data:
`)
	var calls [][]string
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(args) >= 2 && args[0] == "volume" && args[1] == "inspect" {
			return "volume not found", errors.New("command failed")
		}
		return "", nil
	}
	if err := newVolumeTestClient(run).Up(context.Background(), composePath, ParseOptions{}, UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if len(calls) != 4 {
		t.Fatalf("calls = %#v, want inspect, create, and two runs", calls)
	}
	for _, call := range calls[2:] {
		if !hasArgument(call, "type=volume,source=demo_data,target=/app-data,readonly") && !hasArgument(call, "type=volume,source=demo_data,target=/worker-data,readonly") {
			t.Fatalf("run args = %#v, want a read-only shared volume mount", call)
		}
	}
}

func TestUpReportsFatalWarningForUnsupportedTmpfsShorthandOwnership(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `services:
  app:
    image: alpine
    tmpfs:
      - /cache:mode=755,uid=1000,gid=1000
`)
	var warnings []string
	err := newVolumeTestClient(func(_ context.Context, args ...string) (string, error) {
		t.Fatalf("unexpected runtime call: %#v", args)
		return "", nil
	}).Up(context.Background(), composePath, ParseOptions{}, UpOptions{
		OnFatalWarning: func(message string) {
			warnings = append(warnings, message)
		},
	})
	if !errors.Is(err, ErrUnsupportedFeature) {
		t.Fatalf("Up() error = %v, want ErrUnsupportedFeature", err)
	}
	want := []string{"tmpfs uid/gid ownership is not supported by Apple container"}
	if !reflect.DeepEqual(warnings, want) {
		t.Fatalf("fatal warnings = %#v, want %#v", warnings, want)
	}
}

func TestDownVolumesKeepsDetachedAnonymousVolumes(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `name: demo
services:
  app:
    image: alpine
`)
	const named = "demo_pgdata"
	const currentAnonymous = "acc-demo-anon-0123456789abcdef0123456789abcdef"
	const detachedAnonymous = "acc-demo-anon-fedcba9876543210fedcba9876543210"
	var calls [][]string
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		switch {
		case reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}):
			return `[{"configuration":{"id":"demo_app_1","labels":{"` + accProjectLabel + `":"demo","` + accServiceLabel + `":"app"}}}]`, nil
		case reflect.DeepEqual(args, []string{"inspect", "demo_app_1"}):
			return `[{"id":"demo_app_1","configuration":{"id":"demo_app_1","mounts":[{"type":{"volume":{"name":"` + currentAnonymous + `"}}}]}}]`, nil
		case reflect.DeepEqual(args, []string{"volume", "list", "--format", "json"}):
			return `[
  {"id":"` + named + `","configuration":{"name":"` + named + `","labels":{"` + accProjectLabel + `":"demo","` + accVolumeLabel + `":"pgdata"}}},
  {"id":"` + currentAnonymous + `","configuration":{"name":"` + currentAnonymous + `","labels":{"` + accProjectLabel + `":"demo","` + accVolumeLabel + `":"` + currentAnonymous + `"}}},
  {"id":"` + detachedAnonymous + `","configuration":{"name":"` + detachedAnonymous + `","labels":{"` + accProjectLabel + `":"demo","` + accVolumeLabel + `":"` + detachedAnonymous + `"}}},
  {"id":"foreign","configuration":{"name":"foreign","labels":{"` + accProjectLabel + `":"other","` + accVolumeLabel + `":"foreign"}}}
]`, nil
		default:
			return "", nil
		}
	}

	client := newVolumeTestClient(run)
	if err := client.Down(context.Background(), composePath, ParseOptions{}, DownOptions{Volumes: true}); err != nil {
		t.Fatalf("Down() error = %v", err)
	}
	wantDelete := []string{"volume", "delete", currentAnonymous, named}
	if !hasCall(calls, wantDelete) {
		t.Fatalf("Down() calls = %#v, want %#v", calls, wantDelete)
	}
	for _, call := range calls {
		if len(call) >= 3 && call[0] == "volume" && call[1] == "delete" && hasArgument(call, detachedAnonymous) {
			t.Fatalf("Down() calls = %#v, must retain detached anonymous volume %q", calls, detachedAnonymous)
		}
	}
}

func TestDownVolumesRetainsNamedVolumeMountedByOrphan(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `name: demo
services:
  app:
    image: alpine
`)

	const currentVolume = "demo_current"
	const orphanVolume = "demo_orphan"
	var calls [][]string
	run := func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		switch {
		case reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}):
			return `[
  {"configuration":{"id":"demo_app_1","labels":{"` + accProjectLabel + `":"demo","` + accServiceLabel + `":"app"}}},
  {"configuration":{"id":"demo_old_1","labels":{"` + accProjectLabel + `":"demo","` + accServiceLabel + `":"old"}}}
]`, nil
		case reflect.DeepEqual(args, []string{"inspect", "demo_app_1"}):
			return `[{"configuration":{"id":"demo_app_1","mounts":[{"type":{"volume":{"name":"` + currentVolume + `"}}}]}}]`, nil
		case reflect.DeepEqual(args, []string{"inspect", "demo_old_1"}):
			return `[{"configuration":{"id":"demo_old_1","mounts":[{"type":{"volume":{"name":"` + orphanVolume + `"}}}]}}]`, nil
		case reflect.DeepEqual(args, []string{"volume", "list", "--format", "json"}):
			return `[
  {"id":"` + currentVolume + `","configuration":{"name":"` + currentVolume + `","labels":{"` + accProjectLabel + `":"demo","` + accVolumeLabel + `":"current"}}},
  {"id":"` + orphanVolume + `","configuration":{"name":"` + orphanVolume + `","labels":{"` + accProjectLabel + `":"demo","` + accVolumeLabel + `":"orphan"}}}
]`, nil
		default:
			return "", nil
		}
	}

	client := newVolumeTestClient(run)
	if err := client.Down(context.Background(), composePath, ParseOptions{}, DownOptions{Volumes: true}); err != nil {
		t.Fatalf("Down() error = %v", err)
	}
	if !hasCall(calls, []string{"volume", "delete", currentVolume}) {
		t.Fatalf("Down() calls = %#v, want current named volume deleted", calls)
	}
	for _, call := range calls {
		if len(call) >= 3 && call[0] == "volume" && call[1] == "delete" && hasArgument(call, orphanVolume) {
			t.Fatalf("Down() calls = %#v, must retain named volume %q mounted by orphan", calls, orphanVolume)
		}
	}
}

func TestUpDoesNotCreateAnotherAnonymousVolumeForExistingService(t *testing.T) {
	_, composePath := writeVolumeCompose(t, `name: demo
services:
  app:
    image: alpine
    volumes:
      - /cache
`)
	run := func(_ context.Context, args ...string) (string, error) {
		if reflect.DeepEqual(args, []string{"list", "--format", "json", "--all"}) {
			return `[{"configuration":{"id":"demo_app_1","labels":{"` + accProjectLabel + `":"demo","` + accServiceLabel + `":"app"}}}]`, nil
		}
		t.Fatalf("unexpected runtime call: %#v", args)
		return "", nil
	}

	if err := newVolumeTestClient(run).Up(context.Background(), composePath, ParseOptions{}, UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
}

func newVolumeTestClient(run func(context.Context, ...string) (string, error)) *ComposeClient {
	networkRun := func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "network" && args[1] == "inspect" {
			return "network not found", errors.New("command failed")
		}
		if len(args) >= 2 && args[0] == "network" && args[1] == "list" {
			return "[]", nil
		}
		return "", nil
	}
	return &ComposeClient{containerClient: &container.Client{
		Container: container.NewContainerClient(run, nil),
		Volumes:   container.NewVolumeClient(run),
		Networks:  container.NewNetworkClient(networkRun),
	}}
}

func writeVolumeCompose(t *testing.T, contents string) (string, string) {
	t.Helper()
	projectDir := t.TempDir()
	composePath := filepath.Join(projectDir, "compose.yaml")
	if err := os.WriteFile(composePath, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return projectDir, composePath
}

func hasArgument(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func hasCall(calls [][]string, want []string) bool {
	for _, call := range calls {
		if reflect.DeepEqual(call, want) {
			return true
		}
	}
	return false
}
