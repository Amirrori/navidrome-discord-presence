package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestReplacesOnlyPrivateRanges(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "manifest.json")
	out := filepath.Join(dir, "out.json")
	source := `{"name":"x","permissions":{"http":{"requiredHosts":
		["10.0.0.0/8","172.16.0.0/12","192.168.0.0/16","fc00::/7","coverartarchive.org","uguu.se"]}}}`
	if err := os.WriteFile(in, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := run(in, out, []string{"presence.example.com", "192.0.2.16"}); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(out)
	var manifest struct {
		Permissions struct {
			HTTP struct {
				RequiredHosts []string `json:"requiredHosts"`
			} `json:"http"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	want := []string{"presence.example.com", "192.0.2.16", "coverartarchive.org", "uguu.se"}
	if got := manifest.Permissions.HTTP.RequiredHosts; !slices.Equal(got, want) {
		t.Errorf("requiredHosts = %v, want %v", got, want)
	}
}

func TestRequiresHosts(t *testing.T) {
	if err := run("manifest.json", "out.json", nil); err == nil {
		t.Error("missing hosts must be an error")
	}
}

func TestIsPrivateRange(t *testing.T) {
	cases := map[string]bool{
		"10.0.0.0/8":     true,
		"192.168.0.0/16": true,
		"fc00::/7":       true,
		"192.0.2.16":     false, // a single address is kept as given
		"192.0.2.16/32":  false,
		"8.8.8.0/24":     false,
		"uguu.se":        false,
	}
	for entry, want := range cases {
		if got := isPrivateRange(entry); got != want {
			t.Errorf("isPrivateRange(%q) = %v, want %v", entry, got, want)
		}
	}
}
