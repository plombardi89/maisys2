package main

import (
	"testing"

	"github.com/plombardi89/maisys2/internal/system"
)

func TestIsHelper(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{[]string{system.HelperCommand, "--pipe", "p", "--secret", "s"}, true},
		{[]string{"sync"}, false},
		{[]string{"sync", system.HelperCommand}, false},
		{nil, false},
	}
	for _, tt := range tests {
		if got := isHelper(tt.args); got != tt.want {
			t.Errorf("isHelper(%q) = %t, want %t", tt.args, got, tt.want)
		}
	}
}
