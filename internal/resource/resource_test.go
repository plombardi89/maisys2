package resource

import (
	"context"
	"errors"
	"testing"

	"github.com/plombardi89/maisys2/internal/state"
)

func TestActions(t *testing.T) {
	tests := []struct {
		action  Action
		symbol  string
		name    string
		applies bool
	}{
		{NoChange, "", "no change", false},
		{Create, "+", "create", true},
		{Update, "~", "update", true},
		{Remove, "-", "remove", true},
		{Warn, "!", "warn", true},
		{Unknown, "?", "unknown", true},
		{Orphan, "?", "orphan", false},
		{Action(99), "", "Action(99)", false},
	}
	for _, tt := range tests {
		if got := tt.action.Symbol(); got != tt.symbol {
			t.Errorf("%s.Symbol() = %q, want %q", tt.name, got, tt.symbol)
		}
		if got := tt.action.String(); got != tt.name {
			t.Errorf("String() = %q, want %q", got, tt.name)
		}
		if got := tt.action.Applies(); got != tt.applies {
			t.Errorf("%s.Applies() = %t, want %t", tt.name, got, tt.applies)
		}
	}
}

func TestDescribe(t *testing.T) {
	tests := []struct{ id, want string }{
		{"repo:hashicorp", "repo hashicorp"},
		{"file:~/.config/nvim/init.lua", "file ~/.config/nvim/init.lua"},
		{"go-install:golang.org/x/tools/cmd/goimports", "go-install golang.org/x/tools/cmd/goimports"},
		{"git-setting:credential.helper", "git-setting credential.helper"},
		{"sudoers", "sudoers"},
		{":odd", ":odd"},
		{"odd:", "odd:"},
	}
	for _, tt := range tests {
		if got := Describe(tt.id); got != tt.want {
			t.Errorf("Describe(%q) = %q, want %q", tt.id, got, tt.want)
		}
	}
}

type remover string

func (r remover) Kind() string { return string(r) }

func (remover) CheckRemove(context.Context, *Runtime, state.Record) (Change, error) {
	return Change{}, nil
}

func (remover) Remove(context.Context, *Runtime, state.Record) error { return nil }

func TestNewRemovers(t *testing.T) {
	rs := NewRemovers(remover("tool"), remover("file"))
	if len(rs) != 2 || rs["tool"] != remover("tool") || rs["file"] != remover("file") {
		t.Errorf("NewRemovers() = %v, want both removers by kind", rs)
	}

	defer func() {
		if recover() == nil {
			t.Error("NewRemovers() with two removers of one kind didn't panic")
		}
	}()
	NewRemovers(remover("tool"), remover("tool"))
}

func TestRuntimeShared(t *testing.T) {
	type key struct{}
	rt := &Runtime{}
	calls := 0
	create := func() (any, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("not yet")
		}
		return calls, nil
	}
	if _, err := rt.Shared(key{}, create); err == nil {
		t.Fatal("Shared() didn't return create's error")
	}
	for range 2 {
		if v, err := rt.Shared(key{}, create); err != nil || v != 2 {
			t.Errorf("Shared() = %v, %v; want 2, made once after the failure", v, err)
		}
	}

	seeded := &Runtime{}
	seeded.SetShared(key{}, "made before")
	if v, _ := seeded.Shared(key{}, create); v != "made before" {
		t.Errorf("Shared() = %v, want the value set before", v)
	}
}
