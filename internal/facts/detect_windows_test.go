//go:build windows

package facts

import (
	"testing"

	"golang.org/x/sys/windows"

	"github.com/plombardi89/maisys2/internal/system"
)

// TestDetectWindowsLive detects the details of the Windows host running
// the tests. CI's Windows runners run as an admin, elevated.
func TestDetectWindowsLive(t *testing.T) {
	w, err := DetectWindows(t.Context(), system.NewRunner(system.Options{}))
	if err != nil {
		t.Fatalf("DetectWindows: %v", err)
	}
	t.Logf("detected %+v", *w)
	if w.Elevated && !w.Admin {
		t.Error("Elevated = true, but Admin = false")
	}
	if want := windows.GetCurrentProcessToken().IsElevated(); w.Elevated != want {
		t.Errorf("Elevated = %t, want %t", w.Elevated, want)
	}
	for _, d := range w.Distros {
		if d.Name == "" || d.ID == "" {
			t.Errorf("distro %+v has no name or ID", d)
		}
	}
}

// TestHasGroup checks that the access token lists the groups everyone is
// in, and not one nobody is.
func TestHasGroup(t *testing.T) {
	token := windows.GetCurrentProcessToken()
	for _, tt := range []struct {
		sid  windows.WELL_KNOWN_SID_TYPE
		want bool
	}{
		{windows.WinWorldSid, true},
		{windows.WinAuthenticatedUserSid, true},
		{windows.WinAnonymousSid, false},
	} {
		sid, err := windows.CreateWellKnownSid(tt.sid)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := hasGroup(token, sid); err != nil || got != tt.want {
			t.Errorf("hasGroup(%s) = %t, %v; want %t", sid, got, err, tt.want)
		}
	}
}

// TestLiveRegistry reads a key every Windows has.
func TestLiveRegistry(t *testing.T) {
	reg := liveRegistry{}
	const key = `Software\Microsoft`
	subs, err := reg.SubKeys(key)
	if err != nil || len(subs) == 0 {
		t.Errorf("SubKeys(%s) = %q, %v; want some", key, subs, err)
	}
	if subs, err := reg.SubKeys(`Software\maisys-test\missing`); err != nil || subs != nil {
		t.Errorf("SubKeys of a missing key = %q, %v; want none", subs, err)
	}
	if _, ok, err := reg.String(key, "maisys-test-missing"); ok || err != nil {
		t.Errorf("String() of a missing value = %t, %v; want false, nil", ok, err)
	}
	if _, ok, err := reg.Integer(`Software\maisys-test\missing`, "x"); ok || err != nil {
		t.Errorf("Integer() of a missing key = %t, %v; want false, nil", ok, err)
	}
}
