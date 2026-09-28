package backup

import (
	"testing"
)

// FuzzDecode: arbitrary bytes never panic the decoder, and whatever decodes is
// a version-1 document within the row caps.
func FuzzDecode(f *testing.F) {
	f.Add([]byte(`{"version":1,"exported_at":"2026-09-28T10:00:00Z","task_types":[],"tasks":[],"executions":[]}`))
	f.Add([]byte(`{"version":1,"tasks":[{"id":"x","payload":{"a":[1,2,{"b":null}]}}]}`))
	f.Add([]byte(`{"version":2}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		doc, err := Decode(raw)
		if err != nil {
			return
		}
		if doc.Version != SchemaVersion || len(doc.Tasks) > MaxRows || len(doc.Executions) > MaxRows || len(doc.TaskTypes) > MaxRows {
			t.Fatalf("decoded an invalid document: %+v", doc)
		}
	})
}
