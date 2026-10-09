package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/kunalvirwal/apple-container-compose/pkg/compose"
)

func TestUpDisplaysEventsBuildsAndPullsWhenDetached(t *testing.T) {
	for _, noColor := range []bool{false, true} {
		name := "colored"
		if noColor {
			name = "no color"
		}
		t.Run(name, func(t *testing.T) {
			root := newRootCommand(
				func() (composeService, error) { return fatalDiagnosticComposeClient{}, nil },
				func(_ context.Context, _ string, _ compose.ParseOptions, opts compose.UpOptions, _ bool) error {
					if opts.Attach {
						t.Fatal("detached up enabled service log attachment")
					}
					if err := writeReconcileLine(opts.Output, "Preparing Compose project"); err != nil {
						return err
					}
					// Progress without a newline must be visible immediately.
					if _, err := io.WriteString(opts.BuildOutput, "#1 building image\r"); err != nil {
						return err
					}
					_, err := io.WriteString(opts.RuntimeOutput, "pulling alpine\r")
					return err
				},
				func(context.Context, string, compose.ParseOptions, compose.DownOptions) error { return nil },
			)
			var output bytes.Buffer
			root.SetOut(&output)
			args := []string{"up", "-d", "--build", "--file", "compose.yaml"}
			if noColor {
				args = append(args, "--no-color")
			}
			root.SetArgs(args)
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			for _, item := range []struct{ message, color string }{
				{"[ACC] Preparing Compose project\n", ansiPurple},
				{"#1 building image\r", ansiBlue},
				{"pulling alpine\r", ansiBlue},
			} {
				message := item.message
				if !noColor {
					message = item.color + message + ansiReset
				}
				if !strings.Contains(output.String(), message) {
					t.Fatalf("missing %q in output %q", message, output.String())
				}
			}
			if got := strings.Contains(output.String(), ansiPurple); got == noColor {
				t.Fatalf("purple output = %v with noColor = %v", got, noColor)
			}
		})
	}
}

func TestUpMarksFatalDiagnosticAsReported(t *testing.T) {
	client := fatalDiagnosticComposeClient{}
	root := newRootCommand(
		func() (composeService, error) { return client, nil },
		func(ctx context.Context, path string, parseOpts compose.ParseOptions, opts compose.UpOptions, _ bool) error {
			return client.Up(ctx, path, parseOpts, opts)
		},
		func(context.Context, string, compose.ParseOptions, compose.DownOptions) error { return nil },
	)
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs([]string{"up", "--file", "compose.yaml"})

	err := root.Execute()
	if !errors.Is(err, compose.ErrUnsupportedFeature) {
		t.Fatalf("Execute() error = %v, want ErrUnsupportedFeature", err)
	}
	if !IsErrorReported(err) {
		t.Fatal("Execute() error was not marked as already reported")
	}
	if got, want := output.String(), "\x1b[91m[Unsupported]: tmpfs uid/gid ownership is not supported by Apple container\n\x1b[0m"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestUpPassesNoCoreDNSOption(t *testing.T) {
	noCoreDNS := false
	client := fatalDiagnosticComposeClient{}
	root := newRootCommand(
		func() (composeService, error) { return client, nil },
		func(_ context.Context, _ string, _ compose.ParseOptions, _ compose.UpOptions, skipCoreDNS bool) error {
			noCoreDNS = skipCoreDNS
			return nil
		},
		func(context.Context, string, compose.ParseOptions, compose.DownOptions) error { return nil },
	)
	root.SetArgs([]string{"up", "--no-coredns", "--file", "compose.yaml"})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !noCoreDNS {
		t.Fatal("up command did not receive NoCoreDNS")
	}
}

func TestUpPassesReconciliationOptions(t *testing.T) {
	var got compose.UpOptions
	root := newRootCommand(
		func() (composeService, error) { return fatalDiagnosticComposeClient{}, nil },
		func(_ context.Context, _ string, _ compose.ParseOptions, opts compose.UpOptions, _ bool) error {
			got = opts
			return nil
		},
		func(context.Context, string, compose.ParseOptions, compose.DownOptions) error { return nil },
	)
	root.SetArgs([]string{"up", "-V", "--remove-orphans", "--file", "compose.yaml"})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !got.RenewAnonymousVolumes || !got.RemoveOrphans {
		t.Fatalf("UpOptions = %#v, want RenewAnonymousVolumes and RemoveOrphans", got)
	}
}

func TestDownPassesOptions(t *testing.T) {
	var got compose.DownOptions
	root := newRootCommand(
		func() (composeService, error) { return fatalDiagnosticComposeClient{}, nil },
		func(context.Context, string, compose.ParseOptions, compose.UpOptions, bool) error { return nil },
		func(_ context.Context, _ string, _ compose.ParseOptions, opts compose.DownOptions) error {
			got = opts
			return nil
		},
	)
	root.SetArgs([]string{"down", "--remove-orphans", "--volumes", "--file", "compose.yaml"})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !got.RemoveOrphans || !got.Volumes {
		t.Fatalf("DownOptions = %#v, want RemoveOrphans and Volumes", got)
	}
}

type fatalDiagnosticComposeClient struct{}

func (fatalDiagnosticComposeClient) Up(_ context.Context, _ string, _ compose.ParseOptions, opts compose.UpOptions) error {
	opts.OnFatalWarning("tmpfs uid/gid ownership is not supported by Apple container")
	return compose.ErrUnsupportedFeature
}

func (fatalDiagnosticComposeClient) Down(context.Context, string, compose.ParseOptions, compose.DownOptions) error {
	return nil
}

func (fatalDiagnosticComposeClient) BuildImages(context.Context, string, compose.ParseOptions, compose.BuildOptions) error {
	return nil
}

func (fatalDiagnosticComposeClient) Logs(context.Context, string, compose.ParseOptions, compose.LogsOptions) error {
	return nil
}
