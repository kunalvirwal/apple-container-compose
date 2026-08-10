package container

import "errors"

var (
	ErrContainerNotFound   = errors.New("container cli not found")
	ErrSystemNotRunning    = errors.New("container system not running")
	ErrInvalidOptions      = errors.New("invalid options provided")
	ErrContainerNameExists = errors.New("container with this name already exists")
)
