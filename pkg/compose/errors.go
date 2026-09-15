package compose

import "errors"

var (
	// ErrComposeFilePathEmpty indicates a missing compose file path.
	ErrComposeFilePathEmpty = errors.New("compose file path cannot be empty")
	// ErrContainerClientNil indicates that ComposeClient was not initialized properly.
	ErrContainerClientNil = errors.New("container client cannot be nil")
	// ErrClientOptionNil indicates that a nil client option was supplied.
	ErrClientOptionNil = errors.New("compose client option cannot be nil")
	// ErrServiceRegistryNil indicates that WithServiceRegistry received nil.
	ErrServiceRegistryNil = errors.New("service registry cannot be nil")
	// ErrServiceNotFound indicates a requested service is absent from the loaded project.
	ErrServiceNotFound = errors.New("service not found in compose project")
	// ErrUnsupportedFeature indicates the compose feature is not implemented yet.
	ErrUnsupportedFeature = errors.New("unsupported compose feature")
)
