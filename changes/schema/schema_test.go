package schema

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestValidExamples(t *testing.T) {
	files, err := filepath.Glob("testdata/valid/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no examples in testdata/valid")
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(data); err != nil {
				t.Error(err)
			}
		})
	}
}

// Each case starts from testdata/valid/deploy.json, breaks one thing and
// expects a failure for that reason: the keyword that failed and where.
func TestInvalid(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(ev map[string]any)
		at      string // JSON pointer into the event
		keyword string
	}{
		{"id with a space", set("id", "deploy 42"), "/id", "pattern"},
		{"id starting with a dash", set("id", "-42"), "/id", "pattern"},
		{"id too long", set("id", strings.Repeat("a", 129)), "/id", "pattern"},
		{"time without offset", set("time", "2026-12-01T14:03:12"), "/time", "format"},
		{"time as a date", set("time", "2026-12-01"), "/time", "format"},
		{"time as unix seconds", set("time", 1764597792), "/time", "type"},
		{"unknown kind", set("kind", "rollback"), "/kind", "enum"},
		{"missing time", del("time"), "", "required"},
		{"missing kind", del("kind"), "", "required"},
		{"missing environment", del("environment"), "", "required"},
		{"empty environment", set("environment", ""), "/environment", "minLength"},
		{"missing source", del("source"), "", "required"},
		{"empty service", set("service", ""), "/service", "minLength"},
		{"empty description", set("description", ""), "/description", "minLength"},
		{"server-set field", set("received_at", "2026-12-01T14:03:13Z"), "", "additionalProperties"},
		{"unknown field", set("version", "1.0.1"), "", "additionalProperties"},
		{"attribute that is not a string", setAttr("replicas", 3), "/attributes/replicas", "type"},
		{"attribute value too long", setAttr("vcs.ref.head.name", strings.Repeat("x", 4097)), "/attributes/vcs.ref.head.name", "maxLength"},
		{"attribute key with a space", setAttr("build id", "7"), "/attributes", "additionalProperties"},

		{"deploy without service", del("service"), "", "required"},
		{"deploy without attributes", del("attributes"), "", "required"},
		{"deploy without version.to", delAttr("version.to"), "/attributes", "required"},
		{"deploy without version.from", delAttr("version.from"), "/attributes", "required"},
		{"deploy with empty version.to", setAttr("version.to", ""), "/attributes/version.to", "minLength"},

		{"flag without attributes", all(set("kind", "flag"), del("attributes")), "", "required"},
		{"flag without key", flag(delAttr("flag.key")), "/attributes", "required"},
		{"flag with empty key", flag(setAttr("flag.key", "")), "/attributes/flag.key", "minLength"},
		{"flag without old value", flag(delAttr("flag.value.from")), "/attributes", "required"},
		{"flag without new value", flag(delAttr("flag.value.to")), "/attributes", "required"},

		{"note without description", set("kind", "note"), "", "required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := baseEvent(t)
			tt.mutate(ev)
			data, err := json.Marshal(ev)
			if err != nil {
				t.Fatal(err)
			}
			got := failures(t, Validate(data))
			want := tt.keyword + " at " + tt.at
			for _, g := range got {
				if g == want {
					return
				}
			}
			t.Errorf("want %q among %q", want, got)
		})
	}
}

func TestNotAnEvent(t *testing.T) {
	for _, doc := range []string{`[]`, `"deploy"`, `null`} {
		if got := failures(t, Validate([]byte(doc))); len(got) != 1 || got[0] != "type at " {
			t.Errorf("%s: got %q", doc, got)
		}
	}
	for _, doc := range []string{``, `{`, `{"kind": "deploy",}`} {
		var verr *jsonschema.ValidationError
		if err := Validate([]byte(doc)); err == nil || errors.As(err, &verr) {
			t.Errorf("%q: want a parse error, got %v", doc, err)
		}
	}
}

func baseEvent(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile("testdata/valid/deploy.json")
	if err != nil {
		t.Fatal(err)
	}
	var ev map[string]any
	if err := json.Unmarshal(data, &ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

// failures flattens a validation error into "keyword at /pointer" strings,
// one per leaf of the error tree.
func failures(t *testing.T, err error) []string {
	t.Helper()
	var verr *jsonschema.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("want a validation error, got %v", err)
	}
	var out []string
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			kw := e.ErrorKind.KeywordPath()
			out = append(out, fmt.Sprintf("%s at %s", kw[len(kw)-1], pointer(e.InstanceLocation)))
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(verr)
	return out
}

func pointer(tokens []string) string {
	if len(tokens) == 0 {
		return ""
	}
	return "/" + strings.Join(tokens, "/")
}

func set(key string, v any) func(map[string]any) {
	return func(ev map[string]any) { ev[key] = v }
}

func del(key string) func(map[string]any) {
	return func(ev map[string]any) { delete(ev, key) }
}

func setAttr(key string, v any) func(map[string]any) {
	return func(ev map[string]any) { ev["attributes"].(map[string]any)[key] = v }
}

func delAttr(key string) func(map[string]any) {
	return func(ev map[string]any) { delete(ev["attributes"].(map[string]any), key) }
}

func all(fs ...func(map[string]any)) func(map[string]any) {
	return func(ev map[string]any) {
		for _, f := range fs {
			f(ev)
		}
	}
}

// flag turns the base deploy into a valid flag event, then applies f.
func flag(f func(map[string]any)) func(map[string]any) {
	return all(
		set("kind", "flag"),
		set("attributes", map[string]any{
			"flag.key":        "payments.latency_ms.postgres",
			"flag.value.from": "0",
			"flag.value.to":   "800",
		}),
		f,
	)
}
