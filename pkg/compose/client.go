package compose

import "github.com/kunalvirwal/apple-container-compose/pkg/container"

// ComposeClient orchestrates multi-service operations using the container SDK.
type ComposeClient struct {
	containerClient *container.Client
	registry        ServiceRegistry
}

// ClientOption configures an optional Compose client integration.
type ClientOption func(*ComposeClient) error

// WithContainerClient supplies the container runtime used by Compose. It is
// useful to applications that also manage ACC infrastructure with the same
// runtime client. When omitted, NewComposeClient constructs the default
// client.
func WithContainerClient(containerClient *container.Client) ClientOption {
	return func(client *ComposeClient) error {
		if containerClient == nil {
			return ErrContainerClientNil
		}
		client.containerClient = containerClient
		return nil
	}
}

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
	client := &ComposeClient{}
	for _, option := range options {
		if option == nil {
			return nil, ErrClientOptionNil
		}
		if err := option(client); err != nil {
			return nil, err
		}
	}
	if client.containerClient == nil {
		containerClient, err := container.NewClient()
		if err != nil {
			return nil, err
		}
		client.containerClient = containerClient
	}
	return client, nil
}
