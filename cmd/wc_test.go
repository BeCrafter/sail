package cmd

import "testing"

func TestIsSpace(t *testing.T) {
	cases := []struct {
		ch   byte
		want bool
	}{
		{' ', true}, {'\t', true}, {'\n', true}, {'\r', true}, {'\v', true}, {'\f', true},
		{'a', false}, {'0', false}, {255, false},
	}
	for _, c := range cases {
		if got := isSpace(c.ch); got != c.want {
			t.Errorf("isSpace(%q) = %v,期望 %v", c.ch, got, c.want)
		}
	}
}
