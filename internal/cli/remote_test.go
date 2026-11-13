package cli

import (
	"reflect"
	"testing"
)

func TestParseRemoteCall(t *testing.T) {
	for _, c := range []struct {
		name string
		env  string // $ZIMON_REMOTE
		args []string
		want *remoteCall // nil: stays here
	}{
		{"no remote", "", []string{"list"}, nil},
		{"--remote", "", []string{"--remote", "nas", "add", "@linux"},
			&remoteCall{name: "nas", args: []string{"add", "@linux"}}},
		{"--remote= after the command", "", []string{"add", "--remote=nas", "-y", "x"},
			&remoteCall{name: "nas", args: []string{"add", "-y", "x"}}},
		{"ZIMON_REMOTE", "nas", []string{"search", "a b"},
			&remoteCall{name: "nas", args: []string{"search", "a b"}}},
		{"--config stays here", "nas", []string{"--config", "/c.toml", "-v", "list"},
			&remoteCall{name: "nas", cfgPath: "/c.toml", args: []string{"-v", "list"}}},
		{"an empty --remote keeps it here", "nas", []string{"--remote=", "list"}, nil},
		{"a command this zimon does not know", "nas", []string{"jobs", "--follow"},
			&remoteCall{name: "nas", args: []string{"jobs", "--follow"}}},
		{"remote stays here", "nas", []string{"remote", "add", "x", "ssh://x"}, nil},
		{"after a global flag too", "nas", []string{"-v", "remote", "test", "nas"}, nil},
		{"completion stays here", "nas", []string{"completion", "fish"}, nil},
		{"help stays here", "nas", []string{"help", "add"}, nil},
		{"--help stays here", "nas", []string{"add", "--help"}, nil},
		{"--version stays here", "nas", []string{"--version"}, nil},
		{"bare zimon stays here", "nas", nil, nil},
		{"completion goes along", "nas", []string{"__complete", "remove", ""},
			&remoteCall{name: "nas", args: []string{"__complete", "remove", ""}, completion: true}},
		{"completing a command's name", "nas", []string{"__complete", "li"}, nil},
		{"completing --remote", "nas", []string{"__complete", "--remote", ""}, nil},
		{"completing remote test", "", []string{"__complete", "--remote", "nas", "remote", "test", ""}, nil},
		{"after --", "nas", []string{"search", "--", "--remote", "x"},
			&remoteCall{name: "nas", args: []string{"search", "--", "--remote", "x"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("ZIMON_REMOTE", c.env)
			got, ok := parseRemoteCall(c.args)
			switch {
			case c.want == nil && ok:
				t.Errorf("parseRemoteCall(%q) = %+v, want it to stay here", c.args, got)
			case c.want != nil && !ok:
				t.Errorf("parseRemoteCall(%q) stays here, want %+v", c.args, *c.want)
			case c.want != nil && !reflect.DeepEqual(got, *c.want):
				t.Errorf("parseRemoteCall(%q) = %+v, want %+v", c.args, got, *c.want)
			}
		})
	}
}
