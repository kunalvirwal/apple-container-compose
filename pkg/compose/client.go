package compose

import "github.com/kunalvirwal/apple-container-compose/pkg/container"

// ComposeClient orchestrates multi-service operations using the container SDK.
type ComposeClient struct {
	containerClient *container.Client
	registry        ServiceRegistry
}

// ClientOption configures an optional Compose client integration.
type ClientOption func(*ComposeClient) error

// WithServiceRegistry configures a registry that receives service runtime
// records during Up and container removals during Down.
func WithServiceRegistry(registry ServiceRegistry) ClientOption {
	return func(client *ComposeClient) error {
		if registry == nil {
			return ErrServiceRegistryNil
		}
		client.registry = registry
		return nil
	}
}

// NewComposeClient creates a Compose client backed by a default container
// client. Without WithServiceRegistry, it performs no registry or filesystem
// state management.
func NewComposeClient(options ...ClientOption) (*ComposeClient, error) {
	containerClient, err := container.NewClient()
	if err != nil {
		return nil, err
	}
	client := &ComposeClient{containerClient: containerClient}
	for _, option := range options {
		if option == nil {
			return nil, ErrClientOptionNil
		}
		if err := option(client); err != nil {
			return nil, err
		}
	}
	return client, nil
}
