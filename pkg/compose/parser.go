package compose

import (
	"context"
	"encoding/json"
	"fmt"
)

// ParseOptions configures compose parsing behavior.
type ParseOptions struct {
	// WorkingDir is used for resolving relative paths and environment files.
	// Defaults to the compose file's directory when empty. It does not change
	// the default project name.
	WorkingDir string
	// ProjectName overrides the Compose project name. When empty, the top-level
	// Compose name is used when present; otherwise the compose file's parent
	// directory name is used.
	ProjectName string
	// Environment provides additional variables used during interpolation.
	Environment map[string]string
	// EnvFiles contains explicit environment files used during interpolation.
	// Files are loaded in the order provided.
	EnvFiles []string
	// DisableOSEnvironment prevents process environment variables from being
	// used during interpolation.
	DisableOSEnvironment bool
	// DisableDotEnv prevents the default .env file from being loaded.
	// Explicit EnvFiles are still honored.
	DisableDotEnv bool
}

// ParseWithOptions validates a compose file and returns it as a JSON object.
func (c *ComposeClient) ParseWithOptionsToJson(ctx context.Context, path string, opts ParseOptions) (map[string]any, error) {
	project, err := c.loadProject(ctx, path, opts)
	if err != nil {
		return nil, err
	}

	jsonBytes, err := json.Marshal(project)
	if err != nil {
		return nil, fmt.Errorf("failed to convert compose model to json: %w", err)
	}

	jsonObject := map[string]any{}
	if err := json.Unmarshal(jsonBytes, &jsonObject); err != nil {
		return nil, fmt.Errorf("failed to decode compose json object: %w", err)
	}

	return jsonObject, nil
}
