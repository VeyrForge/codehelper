package main

import "testing"

func TestIsCodehelperMCPCmd(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"/home/u/go/bin/codehelper", "mcp"}, true},
		{[]string{"codehelper", "mcp"}, true},
		{[]string{"/usr/bin/codehelper-mcp"}, true},
		{[]string{"codehelper", "repair"}, false},
		{[]string{"codehelper"}, false},
		{[]string{"node", "mcp"}, false},
		{nil, false},
	}
	for _, c := range cases {
		if got := isCodehelperMCPCmd(c.args); got != c.want {
			t.Errorf("isCodehelperMCPCmd(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}
