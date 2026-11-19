package main

import "testing"

func TestIDFlagValue(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
		ok   bool
	}{
		{[]string{"info", "--id", "8e52"}, "8e52", true},
		{[]string{"info", "--id=8e52"}, "8e52", true},
		{[]string{"send", "--text", "x"}, "", false},
		{[]string{"info", "--id"}, "", true},
	} {
		got, ok := idFlagValue(tc.args)
		if got != tc.want || ok != tc.ok {
			t.Errorf("idFlagValue(%v) = %q,%v; want %q,%v", tc.args, got, ok, tc.want, tc.ok)
		}
	}
}
