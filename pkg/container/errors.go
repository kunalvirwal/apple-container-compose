package container

import "errors"

var (
	ErrContainerNotFound = errors.New("container cli not found")
	ErrSystemNotRunning  = errors.New("container system not running")
)
