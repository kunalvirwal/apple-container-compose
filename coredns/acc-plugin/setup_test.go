package accplugin

import (
	"testing"

	"github.com/coredns/caddy"
)

func TestParseStatePath(t *testing.T) {
	tests := []struct {
		name     string
		corefile string
		want     string
		wantErr  bool
	}{
		{name: "state path", corefile: "acc /config/state.json", want: "/config/state.json"},
		{name: "missing path", corefile: "acc", wantErr: true},
		{name: "extra argument", corefile: "acc /config/state.json extra", wantErr: true},
		{name: "relative path", corefile: "acc state.json", wantErr: true},
		{name: "following directive", corefile: "acc /config/state.json\nforward . /etc/resolv.conf", want: "/config/state.json"},
		{name: "unsupported block", corefile: "acc /config/state.json {\n unknown value\n}", wantErr: true},
		{name: "missing directive", corefile: "", wantErr: true},
		{name: "empty block", corefile: "acc /config/state.json {\n}", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			controller := caddy.NewTestController("dns", test.corefile)
			path, err := parseStatePath(controller)
			if (err != nil) != test.wantErr {
				t.Fatalf("parseStatePath() error = %v, wantErr %t", err, test.wantErr)
			}
			if path != test.want {
				t.Fatalf("parseStatePath() = %q, want %q", path, test.want)
			}
		})
	}
}
