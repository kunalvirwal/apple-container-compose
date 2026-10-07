package container

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type ImageClient struct {
	run          func(ctx context.Context, args ...string) (string, error)
	runStreaming func(ctx context.Context, out io.Writer, args ...string) (string, error)
}

// ImageSummary is the stable local image identity needed by higher-level
// clients when deciding whether an existing container uses the current image.
type ImageSummary struct {
	Digest string
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

// Exists reports whether an image reference is available in the local image store.
func (i *ImageClient) Exists(ctx context.Context, reference string) (bool, error) {
	_, exists, err := i.InspectSummary(ctx, reference)
	return exists, err
}

// InspectSummary returns the descriptor digest for a local image and whether
// the reference exists. It uses image inspect and therefore never pulls.
func (i *ImageClient) InspectSummary(ctx context.Context, reference string) (ImageSummary, bool, error) {
	if strings.TrimSpace(reference) == "" {
		return ImageSummary{}, false, ErrInvalidOptions
	}
	out, err := i.run(ctx, "image", "inspect", reference)
	if err != nil {
		if isImageNotFoundError(out, err) {
			return ImageSummary{}, false, nil
		}
		return ImageSummary{}, false, err
	}
	if strings.TrimSpace(out) == "" {
		return ImageSummary{}, true, nil
	}
	digest, err := imageDescriptorDigest([]byte(out))
	if err != nil {
		return ImageSummary{}, false, fmt.Errorf("decode image inspect output: %w", err)
	}
	return ImageSummary{Digest: digest}, true, nil
}

func imageDescriptorDigest(data []byte) (string, error) {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return "", err
	}
	return findImageDigest(value), nil
}

func findImageDigest(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range []string{"digest", "Digest"} {
			if digest, ok := typed[key].(string); ok && strings.TrimSpace(digest) != "" {
				return digest
			}
		}
		for _, key := range []string{"descriptor", "Descriptor", "configuration", "Configuration"} {
			if nested, ok := typed[key]; ok {
				if digest := findImageDigest(nested); digest != "" {
					return digest
				}
			}
		}
	case []any:
		for _, item := range typed {
			if digest := findImageDigest(item); digest != "" {
				return digest
			}
		}
	}
	return ""
}

func isImageNotFoundError(output string, err error) bool {
	message := strings.ToLower(output)
	if err != nil {
		message += "\n" + strings.ToLower(err.Error())
	}
	return strings.Contains(message, "not found") || strings.Contains(message, "no such image")
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

	contextPath := opts.ContextDir
	if contextPath == "" {
		contextPath = "."
	}

	if opts.Tag != "" {
		args = append(args, "--tag", opts.Tag)
	}
	if opts.File != "" {
		dockerfilePath := opts.File
		if !filepath.IsAbs(dockerfilePath) {
			dockerfilePath = filepath.Join(contextPath, dockerfilePath)
		}
		if _, err := os.Stat(dockerfilePath); err != nil {
			if os.IsNotExist(err) {
				return "", fmt.Errorf("dockerfile not found: %s", dockerfilePath)
			}
			return "", fmt.Errorf("unable to read file %q: %w", dockerfilePath, err)
		}
		args = append(args, "--file", dockerfilePath)
	} else {
		dockerfilePath := filepath.Join(contextPath, "Dockerfile")
		if _, err := os.Stat(dockerfilePath); err != nil {
			if os.IsNotExist(err) {
				return "", fmt.Errorf("dockerfile not found in context dir: %s", contextPath)
			}
			return "", fmt.Errorf("unable to read Dockerfile in context dir %q: %w", contextPath, err)
		}
		args = append(args, "--file", dockerfilePath)
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
