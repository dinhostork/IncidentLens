package fault

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

// The flags each service reads, by the dependencies it calls. A flag the code
// reads but flags.json lacks would silently never fire.
var topology = map[string]struct {
	deps      []string
	db, cache bool
}{
	"gateway":  {deps: []string{"catalog", "orders"}},
	"catalog":  {deps: []string{"postgres", "redis"}, db: true, cache: true},
	"orders":   {deps: []string{"catalog", "payments", "postgres"}, db: true},
	"payments": {deps: []string{"postgres"}, db: true},
}

func expectedKeys() []string {
	var keys []string
	for svc, s := range topology {
		keys = append(keys, Key(svc, CPUBurnMS, ""), Key(svc, ErrorRate, ""))
		for _, d := range s.deps {
			keys = append(keys, Key(svc, LatencyMS, d), Key(svc, ErrorRate, d))
		}
		if s.db {
			keys = append(keys, Key(svc, PoolSize, ""))
		}
		if s.cache {
			keys = append(keys, Key(svc, CacheDisabled, ""))
		}
	}
	slices.Sort(keys)
	return keys
}

type flagDef struct {
	State          string         `json:"state"`
	Variants       map[string]any `json:"variants"`
	DefaultVariant string         `json:"defaultVariant"`
	Targeting      any            `json:"targeting"`
}

// TestFlagsFile checks deploy/flags/flags.json: every fault the services can
// read is defined, nothing else is, and everything is off.
func TestFlagsFile(t *testing.T) {
	b, err := os.ReadFile("../../../deploy/flags/flags.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Flags map[string]flagDef `json:"flags"`
	}
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}
	var got []string
	for k := range file.Flags {
		got = append(got, k)
	}
	slices.Sort(got)
	if want := expectedKeys(); !slices.Equal(got, want) {
		t.Fatalf("flags.json keys:\n got %v\nwant %v", got, want)
	}
	for k, f := range file.Flags {
		if f.State != "ENABLED" || f.DefaultVariant != "off" || f.Targeting != nil {
			t.Errorf("%s: want state ENABLED, defaultVariant off and no targeting", k)
		}
		off, ok := f.Variants["off"]
		if !ok || (off != false && off != float64(0)) {
			t.Errorf("%s: off variant is %v, want 0 or false", k, off)
		}
	}
}
