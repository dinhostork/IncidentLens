// Package schema validates change events against the JSON Schema in change.schema.json.
package schema

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

//go:embed change.schema.json
var raw []byte

const id = "https://github.com/dinhostork/IncidentLens/blob/main/changes/schema/change.schema.json"

var compiled = mustCompile()

func mustCompile() *jsonschema.Schema {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		panic(fmt.Sprintf("change event schema: %v", err))
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource(id, doc); err != nil {
		panic(fmt.Sprintf("change event schema: %v", err))
	}
	return c.MustCompile(id)
}

// Validate checks one JSON document against the change event schema. A schema
// violation is returned as a *jsonschema.ValidationError.
func Validate(data []byte) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("parse change event: %w", err)
	}
	return compiled.Validate(doc)
}

// Problem is one reason an event does not match the schema.
type Problem struct {
	// Location is a JSON pointer to the offending value, "" for the event itself.
	Location string `json:"location"`
	Message  string `json:"message"`
}

var printer = message.NewPrinter(language.English)

// Problems flattens a validation error returned by Validate into one Problem
// per failed keyword. It returns nil if err is not a validation error.
func Problems(err error) []Problem {
	var verr *jsonschema.ValidationError
	if !errors.As(err, &verr) {
		return nil
	}
	var out []Problem
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			out = append(out, Problem{Location: jsonPointer(e.InstanceLocation), Message: e.ErrorKind.LocalizedString(printer)})
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(verr)
	return out
}

var escaper = strings.NewReplacer("~", "~0", "/", "~1")

func jsonPointer(tokens []string) string {
	var b strings.Builder
	for _, t := range tokens {
		b.WriteByte('/')
		escaper.WriteString(&b, t) //nolint:errcheck // strings.Builder does not fail
	}
	return b.String()
}
