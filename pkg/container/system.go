package container

import (
	"strings"
)

type SystemClient struct {
	run func(args ...string) (string, error)
}

func NewSystemClient(run func(args ...string) (string, error)) SystemClient {
	return SystemClient{
		run: run,
	}
}

func (s *SystemClient) Status() (string, error) {
	out, err := s.run("system", "status")
	if err != nil {
		if strings.Contains(out, "not running") {
			return out, ErrSystemNotRunning
		}
	}
	return out, err
}

func (s *SystemClient) Start() (bool, error) {
	out, err := s.run("system", "start")
	if err != nil {
		return false, err
	}
	if strings.Contains(out, "API server is running") {
		return true, nil
	}
	return false, err
}

func (s *SystemClient) Stop() (bool, error) {
	out, err := s.run("system", "stop")
	if err != nil {
		return false, err
	}
	if strings.Contains(out, "stopping service") || strings.Contains(out, "skipping bootout") {
		return true, nil
	}
	return false, err
}
