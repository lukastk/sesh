package store

import (
	"errors"
	"reflect"
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

func TestThreadMcpArgsRoundTrip(t *testing.T) {
	s := openTestStore(t)
	args := map[string][]string{"soliton": {"--a=1", "--b"}, "other": {"x"}}
	if err := s.InsertThread(api.Thread{ID: "t1", Machine: "m", SessionName: "s1", Cwd: "/", AgentKind: "pi", Tags: []string{}, McpArgs: args}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertThread(api.Thread{ID: "t2", Machine: "m", SessionName: "s2", Cwd: "/", AgentKind: "pi", Tags: []string{}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetThread("t1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.McpArgs, args) {
		t.Errorf("get: %q", got.McpArgs)
	}
	listed, err := s.ListThreads(false)
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range listed {
		if th.ID == "t2" && th.McpArgs != nil {
			t.Errorf("a thread without arguments reads %q, not nil", th.McpArgs)
		}
		if th.ID == "t1" && !reflect.DeepEqual(th.McpArgs, args) {
			t.Errorf("list: %q", th.McpArgs)
		}
	}
}

func TestUpdateThreadMcpArgs(t *testing.T) {
	s := openTestStore(t)
	if err := s.InsertThread(api.Thread{ID: "t1", Machine: "m", SessionName: "s1", Cwd: "/", AgentKind: "pi", Tags: []string{}, McpArgs: map[string][]string{"a": {"1", "2"}, "b": {"3"}}}); err != nil {
		t.Fatal(err)
	}
	revBefore, err := s.ThreadsRev()
	if err != nil {
		t.Fatal(err)
	}
	th, err := s.UpdateThreadMcpArgs("t1", map[string][]string{"a": {"9"}, "c": {"4"}}, []string{"b"})
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string][]string{"a": {"9"}, "c": {"4"}}; !reflect.DeepEqual(th.McpArgs, want) {
		t.Errorf("got %q, want %q", th.McpArgs, want)
	}
	if revAfter, _ := s.ThreadsRev(); revAfter == revBefore {
		t.Error("the change did not bump the threads rev, so peers would not see it")
	}

	var notSet *McpServerNotSetError
	if _, err := s.UpdateThreadMcpArgs("t1", map[string][]string{"z": {"1"}}, []string{"nope"}); !errors.As(err, &notSet) {
		t.Errorf("unset of a missing server: %v", err)
	}
	if th, _ := s.GetThread("t1"); th.McpArgs["z"] != nil {
		t.Error("a refused update wrote its set half")
	}
	if _, err := s.UpdateThreadMcpArgs("absent", nil, nil); !errors.Is(err, ErrThreadNotFound) {
		t.Errorf("absent thread: %v", err)
	}
	th, err = s.UpdateThreadMcpArgs("t1", nil, []string{"a", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if th.McpArgs != nil {
		t.Errorf("emptied map reads %q", th.McpArgs)
	}
}
