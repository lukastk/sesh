package api

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseMcpArgs(t *testing.T) {
	got, err := ParseMcpArgs([]string{"soliton=--agent-file=/a b/c", "soliton=--x", "other=y=z"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"soliton": {"--agent-file=/a b/c", "--x"}, "other": {"y=z"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseMcpArgsRefusals(t *testing.T) {
	for value, want := range map[string]string{
		"noequals": "not <server>=<arg>",
		"=arg":     "invalid server name",
		"a.b=x":    "invalid server name",
		"a b=x":    "invalid server name",
		`a"b=x`:    "invalid server name",
		"soliton=": "empty argument",
		"s=a\x00b": "NUL",
	} {
		if _, err := ParseMcpArgs([]string{value}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want it to mention %q", value, err, want)
		}
	}
	if _, err := ParseMcpArgs(nil); err == nil {
		t.Error("no values was accepted")
	}
}

func TestValidateMcpArgsRefusesAServerWithNoArguments(t *testing.T) {
	if err := ValidateMcpArgs(map[string][]string{"soliton": {}}); err == nil {
		t.Error("an empty list was accepted")
	}
}
