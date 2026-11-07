package tool

import (
	"os"
	"strings"
	"testing"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/state"
)

func TestRemover(t *testing.T) {
	needSymlinks(t)
	u := newUpstream(t, "1.0.0")
	m := newMachine(t, u)
	m.sync(m.tool(u, "1.0.0"))
	rec, _ := m.state.Get("tool:fake")
	// The user has pointed fakectl at something of their own since.
	if err := os.Remove(m.path(".local/bin/fakectl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/usr/bin/true", m.path(".local/bin/fakectl")); err != nil {
		t.Fatal(err)
	}

	var r Remover
	if r.Kind() != "tool" {
		t.Errorf("Kind() = %q", r.Kind())
	}
	c, err := r.CheckRemove(t.Context(), m.rt, rec)
	if err != nil || c.Action != resource.Remove || c.Summary != "fake 1.0.0" || c.Privileged {
		t.Fatalf("CheckRemove() = %+v, %v, want to remove fake 1.0.0", c, err)
	}
	if err := r.Remove(t.Context(), m.rt, rec); err != nil {
		t.Fatal(err)
	}
	if m.exists(".local/share/maisys/tools/fake") || m.exists(".local/bin/fake") {
		t.Errorf("Remove() left the tool's folder or its command")
	}
	if got := m.link(".local/bin/fakectl"); got != "/usr/bin/true" {
		t.Errorf("~/.local/bin/fakectl → %q, want the user's link left alone", got)
	}
	if !m.exists(".local/share/maisys/tools") {
		t.Errorf("Remove() deleted the folder of every tool")
	}

	c, err = r.CheckRemove(t.Context(), m.rt, rec)
	if err != nil || c.Action != resource.NoChange {
		t.Errorf("CheckRemove() once it's gone = %+v, %v, want no change", c, err)
	}
}

func TestRemoverLeftovers(t *testing.T) {
	needSymlinks(t)
	u := newUpstream(t, "1.0.0")
	m := newMachine(t, u)
	m.sync(m.tool(u, "1.0.0"))
	rec, _ := m.state.Get("tool:fake")

	// The folder is gone, but a command still points into it.
	if err := os.RemoveAll(m.toolPath("")); err != nil {
		t.Fatal(err)
	}
	var r Remover
	c, err := r.CheckRemove(t.Context(), m.rt, rec)
	if err != nil || c.Action != resource.Remove {
		t.Fatalf("CheckRemove() = %+v, %v, want to remove what's left", c, err)
	}
	if err := r.Remove(t.Context(), m.rt, rec); err != nil {
		t.Fatal(err)
	}
	if m.exists(".local/bin/fake") || m.exists(".local/bin/fakectl") {
		t.Errorf("Remove() left the dangling commands")
	}
}

func TestRemoverRefuses(t *testing.T) {
	u := newUpstream(t)
	m := newMachine(t, u)
	victim := m.path("important")
	if err := os.MkdirAll(victim+"/1.0.0", 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		paths   []string
		wantErr string
	}{
		{name: "no paths", wantErr: "the state file's record of tool:fake has no paths"},
		{name: "outside the tools folder", paths: []string{victim + "/1.0.0"}, wantErr: "isn't a maisys tool's folder: not deleting it"},
		{name: "the tools folder", paths: []string{m.path(".local/share/maisys/tools/1.0.0")}, wantErr: "isn't a maisys tool's folder"},
		{name: "a hidden folder", paths: []string{m.path(".local/share/maisys/tools/.x/1.0.0")}, wantErr: "isn't a maisys tool's folder"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := state.Record{ID: "tool:fake", Kind: Kind, Paths: tt.paths, Details: map[string]string{"version": "1.0.0"}}
			var r Remover
			if _, err := r.CheckRemove(t.Context(), m.rt, rec); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("CheckRemove() error = %v, want one containing %q", err, tt.wantErr)
			}
			if err := r.Remove(t.Context(), m.rt, rec); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Remove() error = %v, want one containing %q", err, tt.wantErr)
			}
			if !m.exists("important/1.0.0") {
				t.Fatalf("Remove() deleted a folder outside the tools folder")
			}
		})
	}
}
