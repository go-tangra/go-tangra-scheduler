package payload

import (
	"errors"
	"strings"
	"testing"
)

const recipientSchema = `{
  "type": "object",
  "required": ["recipient", "days"],
  "additionalProperties": false,
  "properties": {
    "recipient": {"type": "string", "format": "email"},
    "days": {"type": "integer", "minimum": 1, "maximum": 365},
    "id": {"type": "string", "format": "uuid"},
    "mode": {"enum": ["fast", "slow"]},
    "tags": {"type": "array", "minItems": 1, "maxItems": 2, "items": {"type": "string", "maxLength": 3}},
    "nested": {"type": "object", "properties": {"a/b~c": {"type": "boolean"}}},
    "ref": {"$ref": "#/$defs/positive"}
  },
  "$defs": {"positive": {"type": "number", "exclusiveMinimum": 0}}
}`

func mustCompile(t testing.TB, raw string) *Schema {
	t.Helper()
	s, err := Compile([]byte(raw))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return s
}

func TestCompileEmpty(t *testing.T) {
	for _, raw := range []string{"", "  \n", "null", " null "} {
		s, err := Compile([]byte(raw))
		if s != nil || err != nil {
			t.Fatalf("Compile(%q) = %v, %v; want nil, nil", raw, s, err)
		}
	}
}

func TestCompileErrors(t *testing.T) {
	deep := strings.Repeat(`{"not":`, MaxDepth) + `{}` + strings.Repeat(`}`, MaxDepth)
	tests := []struct {
		name string
		raw  string
		msg  string
	}{
		{"not json", `{"type":`, "not valid JSON"},
		{"trailing data", `{} {}`, "not valid JSON"},
		{"array", `[]`, "must be a JSON object"},
		{"boolean schema", `true`, "must be a JSON object"},
		{"too large", `{"description":"` + strings.Repeat("x", MaxSchemaBytes) + `"}`, "exceeds 65536 bytes"},
		{"too deep", deep, "nesting exceeds 32 levels"},
		{"bad type keyword", `{"type": 5}`, "invalid schema"},
		{"bad minimum", `{"minimum": "x"}`, "invalid schema"},
		{"bad required", `{"required": "a"}`, "invalid schema"},
		{"http ref", `{"$ref": "http://example.com/s.json"}`, "external references are not allowed"},
		{"https ref", `{"properties": {"a": {"$ref": "https://example.com/s.json#/x"}}}`, "external references are not allowed"},
		{"file ref", `{"$ref": "file:///etc/passwd"}`, "external references are not allowed"},
		{"relative ref", `{"$ref": "other.json"}`, "external references are not allowed"},
		{"relative to $id", `{"$id": "http://example.com/root.json", "$ref": "sub.json"}`, "external references are not allowed"},
		{"custom metaschema", `{"$schema": "http://example.com/meta.json"}`, "external references are not allowed"},
		{"missing local ref", `{"$ref": "#/$defs/none"}`, "invalid schema"},
		{"bad pattern", `{"pattern": "(?=x)"}`, "invalid schema"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := Compile([]byte(tt.raw))
			if err == nil {
				t.Fatalf("Compile accepted: %v", s)
			}
			if !errors.Is(err, ErrInvalidSchema) {
				t.Fatalf("error %v does not wrap ErrInvalidSchema", err)
			}
			if !strings.Contains(err.Error(), tt.msg) {
				t.Fatalf("error %q does not contain %q", err, tt.msg)
			}
		})
	}
}

func TestCompileAtLimits(t *testing.T) {
	deep := strings.Repeat(`{"not":`, MaxDepth-1) + `{}` + strings.Repeat(`}`, MaxDepth-1)
	mustCompile(t, deep)
	mustCompile(t, `{}`)
	mustCompile(t, `{"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object"}`)
	mustCompile(t, `{"$schema": "http://json-schema.org/draft-07/schema#", "type": "object"}`)
	mustCompile(t, recipientSchema)
}

func TestValidate(t *testing.T) {
	s := mustCompile(t, recipientSchema)
	tests := []struct {
		name    string
		payload string
		field   string
		msg     string
	}{
		{"missing required", `{"days": 3}`, "/recipient", "required property missing"},
		{"missing both", `{}`, "/recipient", "required property missing"},
		{"wrong type", `{"recipient": "a@example.com", "days": "3"}`, "/days", "must be integer"},
		{"not integer", `{"recipient": "a@example.com", "days": 1.5}`, "/days", "must be integer"},
		{"below minimum", `{"recipient": "a@example.com", "days": 0}`, "/days", "must be >= 1"},
		{"above maximum", `{"recipient": "a@example.com", "days": 366}`, "/days", "must be <= 365"},
		{"bad email", `{"recipient": "secret-value", "days": 3}`, "/recipient", "must be a valid email"},
		{"bad uuid", `{"recipient": "a@example.com", "days": 3, "id": "secret-value"}`, "/id", "must be a valid uuid"},
		{"enum", `{"recipient": "a@example.com", "days": 3, "mode": "secret-value"}`, "/mode", "must be one of the allowed values"},
		{"additional", `{"recipient": "a@example.com", "days": 3, "zz": 1, "extra": "secret-value"}`, "/extra", "property not allowed"},
		{"min items", `{"recipient": "a@example.com", "days": 3, "tags": []}`, "/tags", "must have at least 1 items"},
		{"max items", `{"recipient": "a@example.com", "days": 3, "tags": ["a","b","c"]}`, "/tags", "must have at most 2 items"},
		{"items type", `{"recipient": "a@example.com", "days": 3, "tags": ["a", 7]}`, "/tags/1", "must be string"},
		{"items length", `{"recipient": "a@example.com", "days": 3, "tags": ["secret-value"]}`, "/tags/0", "must be at most 3 characters long"},
		{"escaped pointer", `{"recipient": "a@example.com", "days": 3, "nested": {"a/b~c": "secret-value"}}`, "/nested/a~1b~0c", "must be boolean"},
		{"local ref", `{"recipient": "a@example.com", "days": 3, "ref": 0}`, "/ref", "must be > 0"},
		{"deepest wins", `{"days": 0, "tags": ["a", 1]}`, "/tags/1", "must be string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(s, []byte(tt.payload), 0)
			fe := asFieldError(t, err)
			if fe.Reason != ReasonInvalid || fe.Field != tt.field || fe.Message != tt.msg {
				t.Fatalf("got %+v, want field %q msg %q", fe, tt.field, tt.msg)
			}
			if strings.Contains(fe.Error(), "secret-value") {
				t.Fatalf("message echoes the payload value: %q", fe.Error())
			}
		})
	}
	if err := Validate(s, []byte(`{"recipient": "a@example.com", "days": 30, "id": "0b6f3b8e-8e0a-4f55-9b62-2d1f0d6a9d11", "mode": "fast", "tags": ["a"], "ref": 2.5}`), 0); err != nil {
		t.Fatalf("valid payload refused: %v", err)
	}
}

func TestValidateKeywordMessages(t *testing.T) {
	tests := []struct {
		schema  string
		payload string
		field   string
		msg     string
	}{
		{`{"properties": {"a": {"const": 1}}}`, `{"a": 2}`, "/a", "must equal the allowed value"},
		{`{"properties": {"a": {"maximum": 5, "exclusiveMaximum": 5}}}`, `{"a": 5}`, "/a", "must be < 5"},
		{`{"properties": {"a": {"multipleOf": 0.5}}}`, `{"a": 0.7}`, "/a", "must be a multiple of 0.5"},
		{`{"properties": {"a": {"minLength": 2}}}`, `{"a": "x"}`, "/a", "must be at least 2 characters long"},
		{`{"properties": {"a": {"pattern": "^[a-z]+$"}}}`, `{"a": "X"}`, "/a", `must match pattern "^[a-z]+$"`},
		{`{"properties": {"a": {"uniqueItems": true}}}`, `{"a": [1, 1]}`, "/a", "items must be unique"},
		{`{"minProperties": 2}`, `{"a": 1}`, "", "must have at least 2 properties"},
		{`{"maxProperties": 1}`, `{"a": 1, "b": 2}`, "", "must have at most 1 properties"},
		{`{"dependentRequired": {"a": ["b"]}}`, `{"a": 1}`, "/b", "required property missing"},
		{`{"properties": {"a": false}}`, `{"a": 1}`, "/a", "does not match the schema"},
		{`{"properties": {"a": {"type": ["string", "null"]}}}`, `{"a": 1}`, "/a", "must be null or string"},
		{`{"properties": {"a": {"format": "date-time"}}}`, `{"a": "yesterday"}`, "/a", "must be a valid date-time"},
		{`{"properties": {"a": {"format": "ipv4"}}}`, `{"a": "300.1.1.1"}`, "/a", "must be a valid ipv4"},
		{`{"properties": {"a": {"oneOf": [{"type": "integer"}, {"type": "number"}]}}}`, `{"a": 1}`, "/a", "does not match the schema"},
	}
	for _, tt := range tests {
		t.Run(tt.schema, func(t *testing.T) {
			fe := asFieldError(t, Validate(mustCompile(t, tt.schema), []byte(tt.payload), 0))
			if fe.Field != tt.field || fe.Message != tt.msg {
				t.Fatalf("got %+v, want field %q msg %q", fe, tt.field, tt.msg)
			}
		})
	}
}

func TestValidateBounds(t *testing.T) {
	s := mustCompile(t, `{"type": "object"}`)
	deep := strings.Repeat(`{"a":`, MaxDepth) + `1` + strings.Repeat(`}`, MaxDepth)
	tooDeep := strings.Repeat(`{"a":`, MaxDepth) + `[]` + strings.Repeat(`}`, MaxDepth)
	tests := []struct {
		name     string
		schema   *Schema
		payload  string
		maxBytes int
		reason   string
		msg      string
	}{
		{"default limit", s, `{"a":"` + strings.Repeat("x", DefaultMaxBytes) + `"}`, 0, ReasonTooLarge, "payload exceeds 65536 bytes"},
		{"custom limit", s, `{"a": 12345}`, 8, ReasonTooLarge, "payload exceeds 8 bytes"},
		{"size before syntax", s, `not json at all`, 4, ReasonTooLarge, "payload exceeds 4 bytes"},
		{"invalid json", s, `{"a": }`, 0, ReasonInvalid, "payload is not valid JSON"},
		{"empty", nil, ``, 0, ReasonInvalid, "payload is not valid JSON"},
		{"trailing", nil, `{} []`, 0, ReasonInvalid, "payload is not valid JSON"},
		{"array root", nil, `[1]`, 0, ReasonInvalid, "payload must be a JSON object"},
		{"string root", s, `"x"`, 0, ReasonInvalid, "payload must be a JSON object"},
		{"null root", nil, `null`, 0, ReasonInvalid, "payload must be a JSON object"},
		{"too deep", nil, tooDeep, 0, ReasonTooDeep, "payload nesting exceeds 32 levels"},
		{"brackets in strings ignored", nil, `{"a": "[[[[{{{{\"]]]"}`, 0, "", ""},
		{"at depth limit", nil, deep, 0, "", ""},
		{"nil schema any object", nil, `{"anything": [1, {"x": null}]}`, 0, "", ""},
		{"custom limit fits", s, `{"a": 1}`, 8, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.schema, []byte(tt.payload), tt.maxBytes)
			if tt.reason == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			fe := asFieldError(t, err)
			if fe.Reason != tt.reason || fe.Field != "" || fe.Message != tt.msg {
				t.Fatalf("got %+v, want %s %q", fe, tt.reason, tt.msg)
			}
		})
	}
}

func TestFieldErrorString(t *testing.T) {
	if got := (&FieldError{Reason: ReasonInvalid, Field: "/a", Message: "must be integer"}).Error(); got != "invalid_payload: /a: must be integer" {
		t.Fatal(got)
	}
	if got := (&FieldError{Reason: ReasonTooLarge, Message: "payload exceeds 8 bytes"}).Error(); got != "payload_too_large: payload exceeds 8 bytes" {
		t.Fatal(got)
	}
}

func TestCanonical(t *testing.T) {
	got, err := Canonical([]byte(" {\n \"b\": [1, 2.50, {\"y\": true, \"x\": null}],\n \"a\": \"<&>\" } "))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"a":"<&>","b":[1,2.50,{"x":null,"y":true}]}`; string(got) != want {
		t.Fatalf("Canonical = %s, want %s", got, want)
	}
	big := `{"n":12345678901234567890123}`
	if got, _ := Canonical([]byte(big)); string(got) != big {
		t.Fatalf("number precision lost: %s", got)
	}
	for _, raw := range []string{``, `{`, `[1]`, `"x"`, `null`} {
		if _, err := Canonical([]byte(raw)); err == nil {
			t.Fatalf("Canonical(%q) accepted", raw)
		}
	}
}

func TestHash(t *testing.T) {
	a := Hash([]byte(`{"type":"object","required":["a"],"properties":{"a":{"type":"string"}}}`))
	b := Hash([]byte("{\n  \"properties\": {\"a\": {\"type\": \"string\"}},\n  \"required\": [\"a\"],\n  \"type\": \"object\"\n}"))
	if a == "" || a != b || len(a) != 64 {
		t.Fatalf("hash not key-order independent: %q vs %q", a, b)
	}
	if c := Hash([]byte(`{"type":"object"}`)); c == a {
		t.Fatal("different schemas share a hash")
	}
	for _, raw := range []string{"", "  ", "null"} {
		if h := Hash([]byte(raw)); h != "" {
			t.Fatalf("Hash(%q) = %q, want empty", raw, h)
		}
	}
	// Invalid JSON still hashes (by its trimmed bytes) so a change is detectable.
	if h1, h2 := Hash([]byte(`{bad`)), Hash([]byte(" {bad ")); h1 == "" || h1 != h2 {
		t.Fatalf("invalid JSON hash: %q %q", h1, h2)
	}
}

func asFieldError(t *testing.T, err error) *FieldError {
	t.Helper()
	var fe *FieldError
	if !errors.As(err, &fe) {
		t.Fatalf("error %T %v is not *FieldError", err, err)
	}
	return fe
}
