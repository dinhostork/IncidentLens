// Package schema validates change events against the JSON Schema in change.schema.json.
package schema

import (
	"bytes"
	_ "embed"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
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
