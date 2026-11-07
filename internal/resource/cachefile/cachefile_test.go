package cachefile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testFile struct {
	FormatVersion int               `json:"format_version"`
	Values        map[string]string `json:"values"`
}

func TestSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache", "test.json")
	want := testFile{FormatVersion: 2, Values: map[string]string{"a": "<b>"}}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"a": "<b>"`) {
		t.Errorf("saved file = %s, want indented JSON without escaped HTML", data)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("the cache folder holds %d files, want just the cache file", len(entries))
	}

	var got testFile
	ok, err := Load(path, 2, &got)
	if err != nil || !ok {
		t.Fatalf("Load() = %t, %v, want true, nil", ok, err)
	}
	if got.Values["a"] != "<b>" {
		t.Errorf("Load() read %+v, want %+v", got, want)
	}
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		content string // "" means no file
		ok      bool
		wantErr string
	}{
		{name: "missing"},
		{name: "current", content: `{"format_version": 2, "values": {"a": "b"}}`, ok: true},
		{name: "not JSON", content: `{"format_version": 2,`},
		{name: "no format version", content: `{"values": {}}`},
		{name: "wrong types", content: `{"format_version": 2, "values": []}`},
		{name: "older format", content: `{"format_version": 1, "values": {"a": "b"}}`},
		{name: "newer format", content: `{"format_version": 3}`, wantErr: "was written by a newer version of maisys (format_version 3; this version reads up to 2): update maisys"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "test.json")
			if tt.content != "" {
				if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var f testFile
			ok, err := Load(path, 2, &f)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load() error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || ok != tt.ok {
				t.Fatalf("Load() = %t, %v, want %t, nil", ok, err, tt.ok)
			}
		})
	}
}
