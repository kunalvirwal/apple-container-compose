package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/kunalvirwal/apple-container-compose/pkg/compose"
)

func TestUpMarksFatalDiagnosticAsReported(t *testing.T) {
	client := fatalDiagnosticComposeClient{}
	root := newRootCommand(
		func() (composeService, error) { return client, nil },
		func(ctx context.Context, path string, parseOpts compose.ParseOptions, opts compose.UpOptions, _ bool) error {
			return client.Up(ctx, path, parseOpts, opts)
		},
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
	)
	root.SetArgs([]string{"up", "--no-coredns", "--file", "compose.yaml"})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !noCoreDNS {
		t.Fatal("up command did not receive NoCoreDNS")
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
