package compose

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/types"
	"go.yaml.in/yaml/v4"
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

	composeName, err := composeProjectName(composePath)
	if err != nil {
		return nil, err
	}

	options := []cli.ProjectOptionsFn{}
	if opts.ProjectName != "" {
		options = append(options, cli.WithName(opts.ProjectName))
	} else if composeName == "" {
		projectName := filepath.Base(filepath.Dir(composePath))
		options = append(options, cli.WithName(projectName))
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
	if err := preserveShortSyntaxVolumeConsistency(project, composePath); err != nil {
		return nil, err
	}

	return project, nil
}

// preserveShortSyntaxVolumeConsistency restores the legacy consistency options
// that compose-go intentionally discards while normalizing short volume syntax.
// ACC does not implement these options, but retaining them lets Up report that
// they are being ignored.
func preserveShortSyntaxVolumeConsistency(project *types.Project, composePath string) error {
	document, err := readRawComposeDocument(composePath)
	if err != nil {
		return err
	}
	for serviceName, rawService := range document.Services {
		service, exists := project.Services[serviceName]
		if !exists {
			continue
		}
		for index, rawVolume := range rawService.Volumes {
			definition, ok := rawVolume.(string)
			if !ok || index >= len(service.Volumes) {
				continue
			}
			if consistency := shortSyntaxVolumeConsistency(definition); consistency != "" {
				service.Volumes[index].Consistency = consistency
			}
		}
		project.Services[serviceName] = service
	}
	return nil
}

func shortSyntaxVolumeConsistency(definition string) string {
	for _, option := range shortSyntaxVolumeOptions(definition) {
		switch option {
		case "cached", "delegated", "consistent":
			return option
		}
	}
	return ""
}

type rawComposeDocument struct {
	Services map[string]rawComposeService `yaml:"services"`
}

type rawComposeService struct {
	Volumes []any `yaml:"volumes"`
}

func readRawComposeDocument(composePath string) (rawComposeDocument, error) {
	contents, err := os.ReadFile(composePath)
	if err != nil {
		return rawComposeDocument{}, fmt.Errorf("read compose file for volume options: %w", err)
	}
	var document rawComposeDocument
	if err := yaml.Unmarshal(contents, &document); err != nil {
		return rawComposeDocument{}, fmt.Errorf("read compose volume options: %w", err)
	}
	return document, nil
}

func shortSyntaxVolumeOptions(definition string) []string {
	fields := strings.Split(definition, ":")
	if len(fields) != 3 {
		return nil
	}
	return strings.Split(fields[2], ",")
}

// composeProjectName returns the top-level Compose name when it is present.
// The actual name value is resolved by compose-go so interpolation and
// validation stay on the shared project-loading path.
func composeProjectName(composePath string) (string, error) {
	contents, err := os.ReadFile(composePath)
	if err != nil {
		return "", fmt.Errorf("failed to read compose file: %w", err)
	}

	var document struct {
		Name string `yaml:"name"`
	}
	if err := yaml.Unmarshal(contents, &document); err != nil {
		return "", fmt.Errorf("failed to read compose project name: %w", err)
	}
	return document.Name, nil
}
