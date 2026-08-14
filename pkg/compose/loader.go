package compose

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/types"
)

// loadProject loads and validates a compose file into a typed project.
func (c *ComposeClient) loadProject(ctx context.Context, composePath string, opts ParseOptions) (*types.Project, error) {
	if composePath == "" {
		return nil, ErrComposeFilePathEmpty
	}

	composePath, err := filepath.Abs(composePath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve compose file path: %w", err)
	}

	if _, err := os.Stat(composePath); err != nil {
		return nil, fmt.Errorf("failed to access compose file: %w", err)
	}

	options := []cli.ProjectOptionsFn{}
	if opts.ProjectName != "" {
		options = append(options, cli.WithName(opts.ProjectName))
	}
	if opts.WorkingDir != "" {
		options = append(options, cli.WithWorkingDirectory(opts.WorkingDir))
	}
	if !opts.DisableOSEnvironment {
		options = append(options, cli.WithOsEnv)
	}
	if len(opts.EnvFiles) > 0 {
		options = append(options, cli.WithEnvFiles(opts.EnvFiles...))
		options = append(options, cli.WithDotEnv)
	} else if !opts.DisableDotEnv {
		options = append(options, cli.WithEnvFiles(), cli.WithDotEnv)
	}
	if len(opts.Environment) > 0 {
		env := make([]string, 0, len(opts.Environment))
		keys := make([]string, 0, len(opts.Environment))
		for key := range opts.Environment {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			env = append(env, key+"="+opts.Environment[key])
		}
		options = append(options, cli.WithEnv(env))
	}

	projectOptions, err := cli.NewProjectOptions([]string{composePath}, options...)
	if err != nil {
		return nil, fmt.Errorf("invalid compose options: %w", err)
	}

	project, err := projectOptions.LoadProject(ctx)
	if err != nil {
		return nil, fmt.Errorf("invalid docker compose file: %w", err)
	}

	return project, nil
}

