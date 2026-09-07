package accplugin

import (
	"net/netip"
	"reflect"
	"testing"
)

func fixtureState() State {
	return State{Version: 1, Containers: []Container{
		{ID: "db", Service: "db", Networks: map[string]NetworkAttachment{
			"db": {Addresses: []string{"10.10.0.2"}},
		}},
		{ID: "api-1", Service: "api", Networks: map[string]NetworkAttachment{
			"db":      {Addresses: []string{"10.10.0.3"}},
			"backend": {Addresses: []string{"10.20.0.3", "fd00:20::3"}, Aliases: []string{"backend-api"}},
		}, Nameservers: []string{"1.1.1.1", "9.9.9.9"}},
		{ID: "api-2", Service: "api", Networks: map[string]NetworkAttachment{
			"db":      {Addresses: []string{"10.10.0.4"}},
			"backend": {Addresses: []string{"10.20.0.4", "fd00:20::4"}, Aliases: []string{"backend-api", "BACKEND-API."}},
		}},
		{ID: "frontend", Service: "frontend", Networks: map[string]NetworkAttachment{
			"frontend": {Addresses: []string{"10.30.0.2"}},
			"backend":  {Addresses: []string{"10.20.0.2", "fd00:20::2"}},
		}},
	}}
}

func TestResolve(t *testing.T) {
	registry := registryFromState(t, fixtureState())
	tests := []struct {
		name, source, query string
		kind                ResolutionKind
		addresses           []string
	}{
		{"replicas on shared network", "10.10.0.2", "API.", ResolutionAnswer, []string{"10.10.0.3", "10.10.0.4"}},
		{"isolated service", "10.10.0.2", "frontend", ResolutionHidden, nil},
		{"alias hidden on other attachment", "10.10.0.2", "backend-api", ResolutionHidden, nil},
		{"cross-project alias", "10.30.0.2", "backend-api", ResolutionAnswer, []string{"10.20.0.3", "10.20.0.4", "fd00:20::3", "fd00:20::4"}},
		{"arrive through db resolve on backend", "10.10.0.3", "frontend", ResolutionAnswer, []string{"10.20.0.2", "fd00:20::2"}},
		{"arrive through backend resolve on db", "10.20.0.3", "db", ResolutionAnswer, []string{"10.10.0.2"}},
		{"IPv6 source gets full membership", "fd00:20::3", "db", ResolutionAnswer, []string{"10.10.0.2"}},
		{"multiple shared networks self resolution", "10.10.0.3", "api", ResolutionAnswer, []string{"10.20.0.3", "10.20.0.4", "fd00:20::3", "fd00:20::4"}},
		{"external", "10.10.0.2", "google.com.", ResolutionUnmanaged, nil},
		{"unknown requester internal", "10.99.0.2", "api", ResolutionUnknownRequester, nil},
		{"unknown requester external", "10.99.0.2", "google.com.", ResolutionUnmanaged, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := registry.Resolve(netip.MustParseAddr(test.source), test.query)
			if result.Kind != test.kind {
				t.Fatalf("kind=%v want %v", result.Kind, test.kind)
			}
			var actual []string
			for _, ip := range result.Addresses {
				actual = append(actual, ip.String())
			}
			if !reflect.DeepEqual(actual, test.addresses) {
				t.Fatalf("addresses=%v want %v", actual, test.addresses)
			}
		})
	}
}

func TestResolveIncludesRequesterNameservers(t *testing.T) {
	registry := registryFromState(t, fixtureState())
	result := registry.Resolve(netip.MustParseAddr("10.10.0.3"), "example.com")
	if result.Kind != ResolutionUnmanaged {
		t.Fatalf("kind=%v want unmanaged", result.Kind)
	}
	var actual []string
	for _, address := range result.Nameservers {
		actual = append(actual, address.String())
	}
	want := []string{"1.1.1.1", "9.9.9.9"}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("nameservers=%v want %v", actual, want)
	}
	result.Nameservers[0] = netip.MustParseAddr("8.8.8.8")
	if got := registry.Resolve(netip.MustParseAddr("10.10.0.3"), "example.com").Nameservers[0].String(); got != "1.1.1.1" {
		t.Fatalf("Resolve exposed live nameserver state: %s", got)
	}
}

func TestResolveDeterministicNetworkSelection(t *testing.T) {
	state := fixtureState()
	state.Containers = append(state.Containers, Container{
		ID: "different-api", Service: "api",
		Networks: map[string]NetworkAttachment{"frontend": {Addresses: []string{"10.30.0.9"}}},
	})
	registry := registryFromState(t, state)
	fromFrontend := registry.Resolve(netip.MustParseAddr("10.30.0.2"), "api")
	fromBackend := registry.Resolve(netip.MustParseAddr("10.20.0.2"), "api")
	if !reflect.DeepEqual(fromFrontend, fromBackend) {
		t.Fatal("selection depends on ingress interface")
	}
	if fromFrontend.Addresses[0].String() != "10.20.0.3" {
		t.Fatal("did not select backend before frontend")
	}
	// Caller mutations must not change the immutable snapshot.
	fromFrontend.Addresses[0] = netip.MustParseAddr("10.99.0.99")
	if registry.Resolve(netip.MustParseAddr("10.30.0.2"), "api").Addresses[0] != fromBackend.Addresses[0] {
		t.Fatal("Resolve exposed live snapshot memory")
	}
}

func registryFromState(t *testing.T, state State) *FileRegistry {
	t.Helper()
	s, err := buildSnapshot(state)
	if err != nil {
		t.Fatal(err)
	}
	r := &FileRegistry{}
	r.current.Store(s)
	return r
}
