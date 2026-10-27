package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s := openDir(t, dir)
	return s, dir
}

func openDir(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPutGetRoundTripAndPersistence(t *testing.T) {
	s, dir := openTemp(t)
	link := Link{DiscordID: "42", DiscordName: "alice#0", RefreshToken: "refresh-secret", LinkedAt: time.Unix(1_700_000_000, 0).UTC()}

	if err := s.Put("alice", link); err != nil {
		t.Fatalf("Put: %v", err)
	}
	s.Close()

	got, err := openDir(t, dir).Get("alice")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != link {
		t.Errorf("got %+v, want %+v", got, link)
	}
}

func TestSecondOpenIsRefused(t *testing.T) {
	_, dir := openTemp(t)
	if _, err := Open(dir); !errors.Is(err, ErrLocked) {
		t.Errorf("want ErrLocked while the store is open, got %v", err)
	}
}

func TestTokensAreEncryptedAtRest(t *testing.T) {
	s, dir := openTemp(t)
	if err := s.Put("alice", Link{DiscordID: "42", RefreshToken: "refresh-secret"}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, linksFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "refresh-secret") {
		t.Error("refresh token is stored in plain text")
	}
}

func TestFilePermissions(t *testing.T) {
	s, dir := openTemp(t)
	if err := s.Put("alice", Link{DiscordID: "42", RefreshToken: "x"}); err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]os.FileMode{keyFileName: 0o400, linksFileName: 0o600, lockFileName: 0o600} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", name, got, want)
		}
	}
}

func TestCiphertextIsBoundToUser(t *testing.T) {
	s, dir := openTemp(t)
	if err := s.Put("alice", Link{DiscordID: "1", RefreshToken: "alice-token"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("mallory", Link{DiscordID: "2", RefreshToken: "mallory-token"}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Copy alice's encrypted token into mallory's record on disk.
	path := filepath.Join(dir, linksFileName)
	data, _ := os.ReadFile(path)
	var file linksFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	m := file.Links["mallory"]
	m.RefreshToken = file.Links["alice"].RefreshToken
	file.Links["mallory"] = m
	data, _ = json.Marshal(file)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := openDir(t, dir).Get("mallory"); err == nil {
		t.Error("a token moved to another user must not decrypt")
	}
}

func TestDeleteAndNotLinked(t *testing.T) {
	s, _ := openTemp(t)
	if err := s.Put("alice", Link{DiscordID: "42", RefreshToken: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("alice"); !errors.Is(err, ErrNotLinked) {
		t.Errorf("want ErrNotLinked, got %v", err)
	}
	if err := s.Delete("nobody"); err != nil {
		t.Errorf("deleting a missing link: %v", err)
	}
}

func TestRecordRefreshUpdatesTokenAndLastUsed(t *testing.T) {
	s, _ := openTemp(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	s.now = func() time.Time { return now }
	if err := s.Put("alice", Link{DiscordID: "42", DiscordName: "alice", RefreshToken: "old"}); err != nil {
		t.Fatal(err)
	}

	if err := s.RecordRefresh("alice", "new"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("alice")
	if got.RefreshToken != "new" || got.DiscordName != "alice" || !got.LastUsed.Equal(now) {
		t.Errorf("got %+v", got)
	}
	if err := s.RecordRefresh("nobody", "x"); !errors.Is(err, ErrNotLinked) {
		t.Errorf("want ErrNotLinked, got %v", err)
	}
}

func TestListAndUnused(t *testing.T) {
	s, _ := openTemp(t)
	old := time.Unix(1_600_000_000, 0).UTC()
	recent := time.Unix(1_800_000_000, 0).UTC()
	links := map[string]Link{
		"carol": {DiscordID: "3", RefreshToken: "x", LinkedAt: old},                   // never used since linking
		"alice": {DiscordID: "1", RefreshToken: "x", LinkedAt: old, LastUsed: recent}, // used recently
		"bob":   {DiscordID: "2", RefreshToken: "x", LinkedAt: recent},                // linked recently
	}
	for user, link := range links {
		if err := s.Put(user, link); err != nil {
			t.Fatal(err)
		}
	}

	list := s.List()
	if len(list) != 3 || list[0].Username != "alice" || list[2].Username != "carol" {
		t.Errorf("List = %+v", list)
	}
	unused := s.Unused(recent.Add(-time.Hour))
	if len(unused) != 1 || unused[0] != "carol" {
		t.Errorf("Unused = %v, want [carol]", unused)
	}
}

func TestRejectsWrongKeySize(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, keyFileName), []byte("short"), 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Error("a key of the wrong size must be rejected")
	}
}
