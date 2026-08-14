package compose

import "github.com/kunalvirwal/apple-container-compose/pkg/container"

// ComposeClient orchestrates multi-service operations using the container SDK.
type ComposeClient struct {
	containerClient *container.Client
}

// NewComposeClient creates a Compose client backed by a default container client.
func NewComposeClient() (*ComposeClient, error) {
	containerClient, err := container.NewClient()
	if err != nil {
		return nil, err
	}
	return &ComposeClient{containerClient: containerClient}, nil
}


