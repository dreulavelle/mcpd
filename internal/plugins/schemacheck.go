package plugins

import (
	"fmt"
	"reflect"
)

// checkSchemaType rejects a type that cannot be described honestly by a
// generated JSON schema.
//
// It exists for one mistake, which is easy to make and expensive to find. A
// `json.RawMessage` field looks like the right way to pass a record through
// without inventing a shape for it — but it is a `[]byte`, so the reflected
// schema says "array of integers 0-255" while `encoding/json` marshals it as
// the object it holds. The declared schema contradicts the value, and a client
// that validates rejects every call. Use `map[string]any` for a record whose
// shape is not known.
//
// Plain `[]byte` is the same bug wearing different clothes: it marshals to a
// base64 string, and reflects to an array of integers.
//
// Caught at registration, which is startup, because a schema that never
// validates is a developer's mistake and should not wait for a caller to find
// it.
func checkSchemaType(plugin, tool, role string, t reflect.Type) error {
	return walkSchemaType(t, role, map[reflect.Type]bool{}, func(path string, bad reflect.Type) error {
		return fmt.Errorf("plugins: %s tool %q has %s %s of type %s, which "+
			"reflects to an array of integers but does not marshal as one; "+
			"use map[string]any for a record with no fixed shape",
			plugin, tool, role, path, bad)
	})
}

// walkSchemaType descends a type looking for byte slices, calling report with
// the path to the first one it finds.
func walkSchemaType(t reflect.Type, path string, seen map[reflect.Type]bool, report func(string, reflect.Type) error) error {
	if t == nil || seen[t] {
		return nil
	}
	seen[t] = true

	switch t.Kind() {
	case reflect.Pointer:
		return walkSchemaType(t.Elem(), path, seen, report)
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return report(path, t)
		}
		return walkSchemaType(t.Elem(), path+"[]", seen, report)
	case reflect.Map:
		return walkSchemaType(t.Elem(), path+"[key]", seen, report)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			// A field json ignores is not in the schema either.
			if f.Tag.Get("json") == "-" {
				continue
			}
			if err := walkSchemaType(f.Type, path+"."+f.Name, seen, report); err != nil {
				return err
			}
		}
	}
	return nil
}
