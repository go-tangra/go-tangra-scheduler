package payload

import (
	"errors"
	"testing"
	"time"
)

func FuzzValidate(f *testing.F) {
	for _, seed := range []struct{ schema, payload string }{
		{recipientSchema, `{"recipient": "a@example.com", "days": 3}`},
		{recipientSchema, `{"days": 0, "tags": ["a", 1]}`},
		{`{"type":"object","properties":{"a":{"pattern":"^x+$"}}}`, `{"a":"xxxxxxxxxxxxxxxxxxy"}`},
		{`{"$ref":"#/$defs/a","$defs":{"a":{"$ref":"#/$defs/a"}}}`, `{}`},
		{`{"$ref":"http://example.com/x"}`, `{}`},
		{``, `{"a":[[[[[]]]]]}`},
		{`{}`, `[`},
		{`null`, `{"a":"\"]]]"}`},
	} {
		f.Add(seed.schema, seed.payload)
	}
	fixed := mustCompile(f, recipientSchema)
	f.Fuzz(func(t *testing.T, schema, payload string) {
		start := time.Now()
		for _, s := range []*Schema{nil, fixed} {
			check(t, Validate(s, []byte(payload), 0))
		}
		if s, err := Compile([]byte(schema)); err == nil {
			check(t, Validate(s, []byte(payload), 0))
			_ = Hash([]byte(schema))
		} else if !errors.Is(err, ErrInvalidSchema) {
			t.Fatalf("Compile error does not wrap ErrInvalidSchema: %v", err)
		}
		if b, err := Canonical([]byte(payload)); err == nil {
			if again, err := Canonical(b); err != nil || string(again) != string(b) {
				t.Fatalf("Canonical not idempotent: %s → %s (%v)", b, again, err)
			}
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("took %v", d)
		}
	})
}

func check(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	var fe *FieldError
	if !errors.As(err, &fe) {
		t.Fatalf("error %T is not *FieldError: %v", err, err)
	}
	switch fe.Reason {
	case ReasonInvalid, ReasonTooLarge, ReasonTooDeep:
	default:
		t.Fatalf("unknown reason %q", fe.Reason)
	}
}
