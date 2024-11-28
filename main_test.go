package main

import (
	"testing"
)

func TestEscape(t *testing.T) {
	tt := []struct {
		input string
		want  string
	}{
		{
			input: "hello",
			want:  "hello",
		},
		{
			input: "hello \"world\"",
			want:  "hello \\\"world\\\"",
		},
		{
			input: "[\"hello \\\"world\\\"\"]",
			want:  "[\\\"hello \\\\\\\"world\\\\\\\"\\\"]",
		},
	}

	for _, tc := range tt {
		t.Run(tc.input, func(t *testing.T) {
			got := escape(tc.input)
			if got != tc.want {
				t.Errorf("got %q; want %q", got, tc.want)
			}
		})
	}
}
