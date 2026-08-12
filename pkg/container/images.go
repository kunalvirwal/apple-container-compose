package container

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

type ImageClient struct {
	run          func(ctx context.Context, args ...string) (string, error)
	runStreaming func(ctx context.Context, out io.Writer, args ...string) (string, error)
}

func NewImageClient(
	run func(ctx context.Context, args ...string) (string, error),
	runStreaming func(ctx context.Context, out io.Writer, args ...string) (string, error),
) ImageClient {
	return ImageClient{
		run:          run,
		runStreaming: runStreaming,
	}
}

// List retrieves a list of images in JSON format. If the quiet flag is set to true, it returns only the image IDs separated by newlines.
func (i *ImageClient) List(ctx context.Context, quiet bool) (string, error) {
	if quiet {
		out, err := i.run(ctx, "image", "list", "--quiet")
		if err != nil && strings.Contains(out, "XPC connection error") {
			return "", ErrSystemNotRunning
		}

		images := make([]string, 0)
		for _, line := range strings.Split(out, "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				images = append(images, trimmed)
			}
		}

		// Serialize slice to JSON ("["alpine:latest","nginx:latest"]")
		jsonBytes, err := json.Marshal(images)
		if err != nil {
			return "", err
		}

		return string(jsonBytes), nil
	}
	out, err := i.run(ctx, "image", "list", "--format", "json")
	if err != nil && strings.Contains(out, "XPC connection error") {
		return "", ErrSystemNotRunning
	}
	return out, err
}

// BuildOptions defines the options for building an image.
type BuildOptions struct {
	// The build context path. Defaults to current directory when empty.
	ContextDir string
	// Name and optionally a tag in the name:tag format.
	Tag string
	// Name of the Dockerfile (Default is "Dockerfile").
	File string
	// Do not use cache when building the image.
	NoCache bool

	// The quiet flag has shown to be unstable when used with container build command
	// so it has be commented out for now
	// Suppress build output and print image ID on success.
	// Quiet bool

	// Target OS for the image.
	OS string
	// Target architecture for the image: amd64 or arm64
	Arch []string
	// Target platform for the image in the format os/arch[/variant].
	Platform string
	// Build arguments in the format key=val.
	BuildArgs map[string]string
	// Tries to pull the latest base image from the registry.
	Pull bool
}

// Build builds an image, streams command output to the provided writer, and returns the image id created.
func (i *ImageClient) Build(ctx context.Context, opts BuildOptions, out io.Writer) (string, error) {
	args := []string{"build"}

	if opts.Tag != "" {
		args = append(args, "--tag", opts.Tag)
	}
	if opts.File != "" {
		if _, err := os.Stat(opts.File); err != nil {
			if os.IsNotExist(err) {
				return "", fmt.Errorf("dockerfile not found: %s", opts.File)
			}
			return "", fmt.Errorf("Unable to read the file %q: %w", opts.File, err)
		}
		args = append(args, "--file", opts.File)
	}
	if opts.NoCache {
		args = append(args, "--no-cache")
	}
	// if opts.Quiet {
	// 	args = append(args, "--quiet")
	// }
	if opts.Platform != "" {
		args = append(args, "--platform", opts.Platform)
	} else {
		if opts.OS != "" {
			args = append(args, "--os", opts.OS)
		}
		if len(opts.Arch) > 0 {
			for _, arch := range opts.Arch {
				if strings.TrimSpace(arch) != "" {
					args = append(args, "--arch", arch)
				}
			}
		}
	}
	if len(opts.BuildArgs) > 0 {
		keys := make([]string, 0, len(opts.BuildArgs))
		for k := range opts.BuildArgs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			args = append(args, "--build-arg", k+"="+opts.BuildArgs[k])
		}
	}
	if opts.Pull {
		args = append(args, "--pull")
	}

	contextPath := opts.ContextDir
	if contextPath == "" {
		contextPath = "."
	}
	args = append(args, contextPath)

	buildOut, err := i.runStreaming(ctx, out, args...)
	if err != nil && strings.Contains(buildOut, "XPC connection error") {
		return buildOut, ErrSystemNotRunning
	}

	return lastNonEmptyLine(buildOut), err
}

func lastNonEmptyLine(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for idx := len(lines) - 1; idx >= 0; idx-- {
		if trimmed := strings.TrimSpace(lines[idx]); trimmed != "" {
			return trimmed
		}
	}

	return ""
}
