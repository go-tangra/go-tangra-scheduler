package store

import (
	"testing"

	"github.com/go-tangra/go-tangra/v4/listquery"
)

func TestListSpecs(t *testing.T) {
	for name, s := range map[string]listquery.Spec{"tasks": TaskList, "executions": ExecutionList} {
		if err := s.Validate(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if r := ListRequest(listquery.Request{}, TaskList); r != (listquery.Request{Page: 1, PageSize: 25, Sort: "name", Order: listquery.Asc}) {
		t.Fatalf("zero request = %+v", r)
	}
	if r := ListRequest(listquery.Request{Page: 2, PageSize: 10, Sort: "duration"}, ExecutionList); r != (listquery.Request{Page: 2, PageSize: 10, Sort: "duration", Order: listquery.Desc}) {
		t.Fatalf("partial request = %+v", r)
	}
	if r := ListRequest(listquery.Request{Page: 3, PageSize: 500, Sort: "nope"}, ExecutionList); r != (listquery.Request{Page: 1, PageSize: 25, Sort: "created_at", Order: listquery.Desc}) {
		t.Fatalf("invalid request = %+v", r)
	}
	if got := (listquery.Request{Sort: "state", Order: listquery.Desc}).OrderBy(TaskList); got !=
		"(CASE WHEN status = 'active' AND enabled THEN 'enabled' WHEN status = 'active' THEN 'stopped' ELSE status END) DESC, id DESC" {
		t.Fatalf("state order = %s", got)
	}
	// NOT NULL columns sort without NULLS LAST (index-backed in both
	// directions); nullable ones keep it.
	if got := (listquery.Request{Sort: "created_at", Order: listquery.Desc}).OrderBy(ExecutionList); got != "created_at DESC, id DESC" {
		t.Fatalf("created_at order = %s", got)
	}
	if got := (listquery.Request{Sort: "next_run_at", Order: listquery.Desc}).OrderBy(TaskList); got != "next_run_at DESC NULLS LAST, id DESC" {
		t.Fatalf("next_run_at order = %s", got)
	}
}
