package compose

import (
	"errors"
	"testing"

	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

func TestNewComposeClientUsesInjectedContainerClient(t *testing.T) {
	runtime := &container.Client{}
	client, err := NewComposeClient(WithContainerClient(runtime))
	if err != nil {
		t.Fatalf("NewComposeClient() error = %v", err)
	}
	if client.containerClient != runtime {
		t.Fatal("NewComposeClient() did not retain the injected container client")
	}
}

func TestWithContainerClientRejectsNil(t *testing.T) {
	_, err := NewComposeClient(WithContainerClient(nil))
	if !errors.Is(err, ErrContainerClientNil) {
		t.Fatalf("NewComposeClient() error = %v, want ErrContainerClientNil", err)
	}
}
