package sdk

import (
	"encoding/json"
	"reflect"
	"strings"
)

// schemaFor derives a JSON Schema from a Go type.
//
// It is deliberately small: enough to describe the parameter structs plugins
// actually write, without pulling in a schema library. Anything it cannot
// describe precisely becomes a permissive object, which is safe because the
// plugin decodes and validates the value regardless -- the schema exists to
// help a model construct a call, not to enforce anything.
func schemaFor[T any]() json.RawMessage {
	var zero T
	schema := describeType(reflect.TypeOf(&zero).Elem(), 0)
	encoded, err := json.Marshal(schema)
	if err != nil {
		return json.RawMessage(`{"type":"object"}`)
	}
	return encoded
}

// maxSchemaDepth bounds recursion. A self-referential type would otherwise
// recurse until the stack gives out.
const maxSchemaDepth = 8

func describeType(t reflect.Type, depth int) map[string]any {
	if depth > maxSchemaDepth || t == nil {
		return map[string]any{}
	}

	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	switch t.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.String:
		return map[string]any{"type": "string"}

	case reflect.Slice, reflect.Array:
		// json.RawMessage is a []byte but carries arbitrary JSON, so it must
		// not be described as an array of integers.
		if t == reflect.TypeOf(json.RawMessage{}) {
			return map[string]any{}
		}
		return map[string]any{
			"type":  "array",
			"items": describeType(t.Elem(), depth+1),
		}

	case reflect.Map:
		return map[string]any{
			"type":                 "object",
			"additionalProperties": describeType(t.Elem(), depth+1),
		}

	case reflect.Struct:
		return describeStruct(t, depth)

	default:
		// Interfaces and anything else: accept whatever arrives and let the
		// plugin's own decoding decide.
		return map[string]any{}
	}
}

func describeStruct(t reflect.Type, depth int) map[string]any {
	properties := map[string]any{}
	var required []string

	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}

		name, omitempty, skip := jsonName(field)
		if skip {
			continue
		}

		schema := describeType(field.Type, depth+1)
		if desc := field.Tag.Get("jsonschema"); desc != "" {
			schema["description"] = desc
		}
		properties[name] = schema

		// A pointer or an omitempty field is optional; everything else the
		// plugin expects to be present.
		if !omitempty && field.Type.Kind() != reflect.Pointer {
			required = append(required, name)
		}
	}

	out := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// jsonName resolves a field's wire name from its json tag.
func jsonName(f reflect.StructField) (name string, omitempty, skip bool) {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return "", false, true
	}

	parts := strings.Split(tag, ",")
	name = parts[0]
	if name == "" {
		name = f.Name
	}
	for _, opt := range parts[1:] {
		if opt == "omitempty" {
			omitempty = true
		}
	}
	return name, omitempty, false
}
