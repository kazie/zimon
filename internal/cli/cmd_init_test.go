package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/kazie/zimon/internal/config"
)

// TestConfigHelpListsEverySetting keeps `zimon config --help` in step with
// config.toml's settings.
func TestConfigHelpListsEverySetting(t *testing.T) {
	keys := settingKeys(reflect.TypeFor[config.Config](), "")
	if len(keys) < 10 {
		t.Fatalf("found only %v", keys)
	}
	// A setting's line starts with its keys ("serve.port, serve.kiwix_serve_path"),
	// then two spaces and what they do.
	listed := map[string]bool{}
	for line := range strings.Lines(configHelp) {
		names, _, ok := strings.Cut(strings.TrimLeft(line, " "), "  ")
		if ok && strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") {
			for k := range strings.SplitSeq(names, ", ") {
				listed[k] = true
			}
		}
	}
	for _, k := range keys {
		if !listed[k] {
			t.Errorf("zimon config --help doesn't list %s", k)
		}
	}
}

// settingKeys lists the dotted toml keys of t's settings: a table (a struct
// whose fields have toml tags) gives its own keys, prefixed with its name.
func settingKeys(t reflect.Type, prefix string) []string {
	var keys []string
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
		if name == "" || name == "-" {
			continue
		}
		if f.Type.Kind() == reflect.Struct && isTable(f.Type) {
			keys = append(keys, settingKeys(f.Type, prefix+name+".")...)
			continue
		}
		keys = append(keys, prefix+name)
	}
	return keys
}

func isTable(t reflect.Type) bool {
	for f := range t.Fields() {
		if f.Tag.Get("toml") != "" {
			return true
		}
	}
	return false
}
