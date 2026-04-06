package tool

import (
	"encoding/json"
	"fmt"
)

// SchemaBuilder helps construct JSON Schema objects for tool inputs.
// Source: Tool.ts inputSchema / inputJSONSchema pattern.
type SchemaBuilder struct {
	schema map[string]any
}

// NewSchemaBuilder creates a schema builder for an object-type input.
func NewSchemaBuilder() *SchemaBuilder {
	return &SchemaBuilder{
		schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
			"required":   []string{},
		},
	}
}

// AddStringProp adds a string property to the schema.
func (b *SchemaBuilder) AddStringProp(name, description string, required bool) *SchemaBuilder {
	return b.addProp(name, map[string]any{
		"type":        "string",
		"description": description,
	}, required)
}

// AddNumberProp adds a number property to the schema.
func (b *SchemaBuilder) AddNumberProp(name, description string, required bool) *SchemaBuilder {
	return b.addProp(name, map[string]any{
		"type":        "number",
		"description": description,
	}, required)
}

// AddBooleanProp adds a boolean property to the schema.
func (b *SchemaBuilder) AddBooleanProp(name, description string, required bool) *SchemaBuilder {
	return b.addProp(name, map[string]any{
		"type":        "boolean",
		"description": description,
	}, required)
}

// AddStringEnumProp adds a string enum property to the schema.
func (b *SchemaBuilder) AddStringEnumProp(name, description string, enumValues []string, required bool) *SchemaBuilder {
	return b.addProp(name, map[string]any{
		"type":        "string",
		"description": description,
		"enum":        enumValues,
	}, required)
}

// AddObjectProp adds a nested object property to the schema.
func (b *SchemaBuilder) AddObjectProp(name, description string, properties map[string]any, required bool) *SchemaBuilder {
	prop := map[string]any{
		"type":        "object",
		"description": description,
		"properties":  properties,
	}
	return b.addProp(name, prop, required)
}

// AddArrayProp adds an array property to the schema.
func (b *SchemaBuilder) AddArrayProp(name, description string, items map[string]any, required bool) *SchemaBuilder {
	prop := map[string]any{
		"type":        "array",
		"description": description,
		"items":       items,
	}
	return b.addProp(name, prop, required)
}

func (b *SchemaBuilder) addProp(name string, prop map[string]any, required bool) *SchemaBuilder {
	props, _ := b.schema["properties"].(map[string]any)
	props[name] = prop
	if required {
		req, _ := b.schema["required"].([]string)
		b.schema["required"] = append(req, name)
	}
	return b
}

// Build returns the schema as json.RawMessage.
func (b *SchemaBuilder) Build() json.RawMessage {
	data, err := json.Marshal(b.schema)
	if err != nil {
		// Should never happen with valid map[string]any
		return json.RawMessage(`{"type":"object"}`)
	}
	return json.RawMessage(data)
}

// ValidateInput checks that a JSON input conforms to the required fields
// of a JSON Schema. Returns nil if valid.
// Source: Tool.ts — Zod validation replaced by JSON Schema required-field check.
func ValidateInput(schema json.RawMessage, input json.RawMessage) error {
	var s struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		return fmt.Errorf("invalid schema: %w", err)
	}

	if len(s.Required) == 0 {
		return nil
	}

	var inputMap map[string]any
	if err := json.Unmarshal(input, &inputMap); err != nil {
		return fmt.Errorf("invalid input JSON: %w", err)
	}

	for _, field := range s.Required {
		if _, ok := inputMap[field]; !ok {
			return fmt.Errorf("missing required field: %s", field)
		}
	}

	return nil
}

// RawSchema creates a json.RawMessage from a map.
// Convenience for inline schema definitions.
func RawSchema(m map[string]any) json.RawMessage {
	data, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(data)
}
