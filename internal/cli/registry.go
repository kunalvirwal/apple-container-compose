package cli

import (
	"context"

	"github.com/kunalvirwal/apple-container-compose/internal/state"
	"github.com/kunalvirwal/apple-container-compose/pkg/compose"
)

type stateServiceRegistry struct {
	store *state.Store
}

func newStateServiceRegistry() (*stateServiceRegistry, error) {
	path, err := state.DefaultPath()
	if err != nil {
		return nil, err
	}
	store, err := state.NewStore(path)
	if err != nil {
		return nil, err
	}
	return &stateServiceRegistry{store: store}, nil
}

func (r *stateServiceRegistry) Ensure(ctx context.Context) error {
	return r.store.Ensure(ctx)
}

func (r *stateServiceRegistry) Upsert(ctx context.Context, runtime compose.ServiceRuntime) error {
	networks := make(map[string]state.NetworkAttachment, len(runtime.Networks))
	for _, network := range runtime.Networks {
		addresses := make([]string, 0, len(network.Addresses))
		for _, address := range network.Addresses {
			addresses = append(addresses, address.String())
		}
		networks[network.Name] = state.NetworkAttachment{
			Addresses: addresses,
			Aliases:   append([]string(nil), network.Aliases...),
		}
	}
	return r.store.Upsert(ctx, state.Container{
		ID:          runtime.ContainerID,
		Service:     runtime.ServiceName,
		Nameservers: append([]string(nil), runtime.Nameservers...),
		Networks:    networks,
	})
}

func (r *stateServiceRegistry) Remove(ctx context.Context, containerIDs []string) error {
	return r.store.Remove(ctx, containerIDs)
}

var _ compose.ServiceRegistry = (*stateServiceRegistry)(nil)
