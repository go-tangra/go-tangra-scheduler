// Package payload compiles task-type JSON Schemas and validates task payloads
// against them with bounded size and nesting.
//
// Schemas are JSON Schema documents (draft 2020-12 unless `$schema` names
// another draft) with format assertions enabled. Only in-document references
// are resolved: every remote, file or relative external reference is refused
// at compile time, so compiling a module-supplied schema never performs I/O.
//
// Validation errors name the offending instance location as a JSON pointer
// and describe the failed keyword without echoing payload values, so they are
// safe to return to the caller and to log.
package payload

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

// Bounds.
const (
	// DefaultMaxBytes is the payload size limit when the caller passes none.
	DefaultMaxBytes = 64 << 10
	// MaxDepth is the deepest accepted nesting of objects and arrays
	// (the root object is level 1), for payloads and schemas.
	MaxDepth = 32
	// MaxSchemaBytes is the largest accepted schema document.
	MaxSchemaBytes = 64 << 10
)

// FieldError reasons.
const (
	ReasonInvalid  = "invalid_payload"
	ReasonTooLarge = "payload_too_large"
	ReasonTooDeep  = "payload_too_deep"
)

// ErrInvalidSchema is wrapped by every Compile error.
var ErrInvalidSchema = errors.New("payload: invalid schema")

// schemaURL is the base URI of every compiled schema; a relative reference
// resolves under it and is refused by the loader.
const schemaURL = "https://scheduler.invalid/payload-schema.json"

// Schema is a compiled payload schema. A nil *Schema accepts any JSON object.
type Schema struct {
	s *jsonschema.Schema
}

// FieldError describes why a payload was refused. Field is a JSON pointer to
// the offending value ("" for the payload itself).
type FieldError struct {
	Reason  string
	Field   string
	Message string
}

func (e *FieldError) Error() string {
	if e.Field == "" {
		return e.Reason + ": " + e.Message
	}
	return e.Reason + ": " + e.Field + ": " + e.Message
}

// refuseLoader refuses every external document.
type refuseLoader struct{}

func (refuseLoader) Load(string) (any, error) {
	return nil, errors.New("external references are not allowed")
}

func isEmpty(raw []byte) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || string(t) == "null"
}

// Compile compiles a JSON Schema. An empty document or `null` means "no
// schema" and returns (nil, nil).
func Compile(raw []byte) (*Schema, error) {
	if isEmpty(raw) {
		return nil, nil
	}
	if len(raw) > MaxSchemaBytes {
		return nil, fmt.Errorf("%w: schema exceeds %d bytes", ErrInvalidSchema, MaxSchemaBytes)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: schema is not valid JSON", ErrInvalidSchema)
	}
	if _, ok := doc.(map[string]any); !ok {
		return nil, fmt.Errorf("%w: schema must be a JSON object", ErrInvalidSchema)
	}
	if depth(raw) > MaxDepth {
		return nil, fmt.Errorf("%w: schema nesting exceeds %d levels", ErrInvalidSchema, MaxDepth)
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(refuseLoader{})
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	// AddResource cannot fail: the URL is a constant, absolute, non-meta URL
	// on a fresh compiler; its error is folded into Compile's.
	addErr := c.AddResource(schemaURL, doc)
	s, err := c.Compile(schemaURL)
	if err = errors.Join(addErr, err); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSchema, err)
	}
	return &Schema{s: s}, nil
}

// Hash returns the SHA-256 (hex) of the canonical form of a schema document,
// independent of key order and whitespace; "" when there is no schema. A
// document that is not valid JSON is hashed by its trimmed bytes.
func Hash(raw []byte) string {
	if isEmpty(raw) {
		return ""
	}
	t := bytes.TrimSpace(raw)
	_, b, err := canonical(t)
	if err != nil {
		b = t
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Canonical returns the compact form of a JSON object with sorted keys and
// numbers kept verbatim.
func Canonical(raw []byte) ([]byte, error) {
	doc, b, err := canonical(raw)
	if err != nil {
		return nil, errors.New("payload: not valid JSON")
	}
	if _, ok := doc.(map[string]any); !ok {
		return nil, errors.New("payload: not a JSON object")
	}
	return b, nil
}

func canonical(raw []byte) (any, []byte, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	err = enc.Encode(doc)
	return doc, bytes.TrimSuffix(buf.Bytes(), []byte("\n")), err
}

// Validate checks a payload: size (maxBytes <= 0 means DefaultMaxBytes), JSON
// syntax, an object at the root, nesting depth, then the schema (nil accepts
// any object). Every error is a *FieldError.
func Validate(s *Schema, raw []byte, maxBytes int) error {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if len(raw) > maxBytes {
		return &FieldError{Reason: ReasonTooLarge, Message: fmt.Sprintf("payload exceeds %d bytes", maxBytes)}
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return &FieldError{Reason: ReasonInvalid, Message: "payload is not valid JSON"}
	}
	if _, ok := doc.(map[string]any); !ok {
		return &FieldError{Reason: ReasonInvalid, Message: "payload must be a JSON object"}
	}
	if depth(raw) > MaxDepth {
		return &FieldError{Reason: ReasonTooDeep, Message: fmt.Sprintf("payload nesting exceeds %d levels", MaxDepth)}
	}
	if s == nil {
		return nil
	}
	if err := s.s.Validate(doc); err != nil {
		return mostSpecific(err)
	}
	return nil
}

// depth returns the deepest object/array nesting of valid JSON text.
func depth(raw []byte) int {
	deepest, d := 0, 0
	inString, escaped := false, false
	for _, c := range raw {
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{', '[':
			d++
			deepest = max(deepest, d)
		case '}', ']':
			d--
		}
	}
	return deepest
}

// mostSpecific reduces a validation error tree to one leaf: the deepest
// instance location, then the smallest pointer, then the smallest message.
func mostSpecific(err error) *FieldError {
	best := &FieldError{Reason: ReasonInvalid, Message: "does not match the schema"}
	var leaves []*FieldError
	var ve *jsonschema.ValidationError
	if errors.As(err, &ve) {
		collect(ve, &leaves)
	}
	if len(leaves) > 0 {
		best = slices.MinFunc(leaves, func(a, b *FieldError) int {
			return cmp.Or(
				cmp.Compare(strings.Count(b.Field, "/"), strings.Count(a.Field, "/")),
				cmp.Compare(a.Field, b.Field),
				cmp.Compare(a.Message, b.Message),
			)
		})
	}
	return best
}

func collect(e *jsonschema.ValidationError, out *[]*FieldError) {
	if len(e.Causes) == 0 {
		field, msg := describe(e.ErrorKind, pointer(e.InstanceLocation))
		*out = append(*out, &FieldError{Reason: ReasonInvalid, Field: field, Message: msg})
		return
	}
	for _, c := range e.Causes {
		collect(c, out)
	}
}

// describe turns a failed keyword into a message that never contains the
// instance value. Property names reported (missing or not allowed) are
// appended to the pointer.
func describe(k jsonschema.ErrorKind, at string) (string, string) {
	switch k := k.(type) {
	case *kind.Required:
		return at + "/" + escape(k.Missing[0]), "required property missing"
	case *kind.DependentRequired:
		return at + "/" + escape(k.Missing[0]), "required property missing"
	case *kind.AdditionalProperties:
		return at + "/" + escape(slices.Min(k.Properties)), "property not allowed"
	case *kind.Type:
		return at, "must be " + strings.Join(k.Want, " or ")
	case *kind.Enum:
		return at, "must be one of the allowed values"
	case *kind.Const:
		return at, "must equal the allowed value"
	case *kind.Format:
		return at, "must be a valid " + k.Want
	case *kind.Minimum:
		return at, "must be >= " + num(k.Want)
	case *kind.Maximum:
		return at, "must be <= " + num(k.Want)
	case *kind.ExclusiveMinimum:
		return at, "must be > " + num(k.Want)
	case *kind.ExclusiveMaximum:
		return at, "must be < " + num(k.Want)
	case *kind.MultipleOf:
		return at, "must be a multiple of " + num(k.Want)
	case *kind.MinLength:
		return at, fmt.Sprintf("must be at least %d characters long", k.Want)
	case *kind.MaxLength:
		return at, fmt.Sprintf("must be at most %d characters long", k.Want)
	case *kind.Pattern:
		return at, fmt.Sprintf("must match pattern %q", k.Want)
	case *kind.MinItems:
		return at, fmt.Sprintf("must have at least %d items", k.Want)
	case *kind.MaxItems:
		return at, fmt.Sprintf("must have at most %d items", k.Want)
	case *kind.UniqueItems:
		return at, "items must be unique"
	case *kind.MinProperties:
		return at, fmt.Sprintf("must have at least %d properties", k.Want)
	case *kind.MaxProperties:
		return at, fmt.Sprintf("must have at most %d properties", k.Want)
	default:
		return at, "does not match the schema"
	}
}

func num(r *big.Rat) string {
	f, _ := r.Float64()
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// pointer renders an instance location as a JSON pointer (RFC 6901).
func pointer(tokens []string) string {
	var b strings.Builder
	for _, t := range tokens {
		b.WriteByte('/')
		b.WriteString(escape(t))
	}
	return b.String()
}

func escape(token string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(token)
}
