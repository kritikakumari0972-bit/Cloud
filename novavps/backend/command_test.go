package main

import (
	"strings"
	"testing"
)

func TestValidateSingleLineCommand(t *testing.T) {
	tests := []struct {
		name    string
		command string
		wantErr string
	}{
		{name: "trims whitespace", command: "  uname -a  "},
		{name: "empty command", command: "   ", wantErr: "command_required"},
		{name: "rejects newline", command: "uname -a\nwhoami", wantErr: "single_line_command_required"},
		{name: "rejects carriage return", command: "uname -a\rwhoami", wantErr: "single_line_command_required"},
		{name: "rejects null byte", command: "uname\x00 -a", wantErr: "single_line_command_required"},
		{name: "rejects long command", command: strings.Repeat("x", 2049), wantErr: "command_too_long"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := validateSingleLineCommand(test.command)
			if test.wantErr != "" {
				if err == nil || err.Error() != test.wantErr {
					t.Fatalf("expected error %q, got %v", test.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != strings.TrimSpace(test.command) {
				t.Fatalf("unexpected normalized command %q", got)
			}
		})
	}
}

func TestBoundedOutput(t *testing.T) {
	output := &boundedOutput{limit: 5}
	if n, err := output.Write([]byte("abcdef")); err != nil || n != 6 {
		t.Fatalf("Write() = (%d, %v), want (6, nil)", n, err)
	}
	if n, err := output.Write([]byte("gh")); err != nil || n != 2 {
		t.Fatalf("second Write() = (%d, %v), want (2, nil)", n, err)
	}
	text, truncated := output.snapshot()
	if text != "abcde" {
		t.Fatalf("captured output = %q, want %q", text, "abcde")
	}
	if !truncated {
		t.Fatal("expected output to be marked truncated")
	}
}
