// Package store keeps the links between Navidrome users and their Discord
// accounts. Refresh tokens are encrypted with AES-256-GCM; the Navidrome
// username is bound to each ciphertext as additional data, so an encrypted
// token can't be moved to another user's record.
//
// The state directory is locked while a Store is open, so the service and the
// admin commands can never overwrite each other's changes.
package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"
)

const (
	keyFileName   = "key"
	linksFileName = "links.json"
	lockFileName  = "lock"
	keySize       = 32
	fileVersion   = 1
)

var (
	ErrNotLinked = errors.New("user has no linked Discord account")
	ErrLocked    = errors.New("the state directory is in use by another process (stop the service first)")
)

type Link struct {
	DiscordID    string
	DiscordName  string
	RefreshToken string
	LinkedAt     time.Time
	// LastUsed is the last time the token was refreshed for a status update.
	LastUsed time.Time
}

type Summary struct {
	Username    string
	DiscordID   string
	DiscordName string
	LinkedAt    time.Time
	LastUsed    time.Time
}

type storedLink struct {
	DiscordID    string    `json:"discordId"`
	DiscordName  string    `json:"discordName,omitempty"`
	RefreshToken string    `json:"refreshToken"`
	LinkedAt     time.Time `json:"linkedAt"`
	LastUsed     time.Time `json:"lastUsed,omitzero"`
}

func (l storedLink) lastActivity() time.Time {
	if l.LastUsed.After(l.LinkedAt) {
		return l.LastUsed
	}
	return l.LinkedAt
}

type linksFile struct {
	Version int                   `json:"version"`
	Links   map[string]storedLink `json:"links"`
}

type Store struct {
	mu    sync.Mutex
	dir   string
	path  string
	lock  *os.File
	aead  cipher.AEAD
	now   func() time.Time
	links map[string]storedLink
}

// Open locks dir, loads the store and creates the encryption key on first
// use. Call Close to release the lock.
func Open(dir string) (*Store, error) {
	lock, err := lockDir(dir)
	if err != nil {
		return nil, err
	}
	s, err := openLocked(dir, lock)
	if err != nil {
		_ = lock.Close() // the open error is the one worth reporting
		return nil, err
	}
	return s, nil
}

func openLocked(dir string, lock *os.File) (*Store, error) {
	key, err := loadOrCreateKey(filepath.Join(dir, keyFileName))
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	s := &Store{
		dir:   dir,
		path:  filepath.Join(dir, linksFileName),
		lock:  lock,
		aead:  aead,
		now:   time.Now,
		links: map[string]storedLink{},
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// Close releases the state directory lock.
func (s *Store) Close() error {
	return s.lock.Close()
}

func (s *Store) Get(username string) (Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored, ok := s.links[username]
	if !ok {
		return Link{}, ErrNotLinked
	}
	token, err := s.decrypt(username, stored.RefreshToken)
	if err != nil {
		return Link{}, fmt.Errorf("decrypting token of %s: %w", username, err)
	}
	return Link{
		DiscordID:    stored.DiscordID,
		DiscordName:  stored.DiscordName,
		RefreshToken: token,
		LinkedAt:     stored.LinkedAt,
		LastUsed:     stored.LastUsed,
	}, nil
}

func (s *Store) Put(username string, link Link) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	encrypted, err := s.encrypt(username, link.RefreshToken)
	if err != nil {
		return err
	}
	return s.update(username, storedLink{
		DiscordID:    link.DiscordID,
		DiscordName:  link.DiscordName,
		RefreshToken: encrypted,
		LinkedAt:     link.LinkedAt,
		LastUsed:     link.LastUsed,
	})
}

// RecordRefresh stores the refresh token returned by a successful refresh
// (Discord may rotate it) and marks the link as used now.
func (s *Store) RecordRefresh(username, refreshToken string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored, ok := s.links[username]
	if !ok {
		return ErrNotLinked
	}
	encrypted, err := s.encrypt(username, refreshToken)
	if err != nil {
		return err
	}
	stored.RefreshToken = encrypted
	stored.LastUsed = s.now().UTC()
	return s.update(username, stored)
}

// Delete removes the link of a Navidrome user. Deleting a missing link is not an error.
func (s *Store) Delete(username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	previous, existed := s.links[username]
	if !existed {
		return nil
	}
	delete(s.links, username)
	if err := s.save(); err != nil {
		s.links[username] = previous
		return err
	}
	return nil
}

func (s *Store) List() []Summary {
	s.mu.Lock()
	defer s.mu.Unlock()

	summaries := make([]Summary, 0, len(s.links))
	for username, l := range s.links {
		summaries = append(summaries, Summary{
			Username:    username,
			DiscordID:   l.DiscordID,
			DiscordName: l.DiscordName,
			LinkedAt:    l.LinkedAt,
			LastUsed:    l.LastUsed,
		})
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].Username < summaries[j].Username })
	return summaries
}

func (s *Store) Unused(cutoff time.Time) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var users []string
	for username, l := range s.links {
		if l.lastActivity().Before(cutoff) {
			users = append(users, username)
		}
	}
	sort.Strings(users)
	return users
}

// update replaces one record and saves, restoring the old record on failure.
func (s *Store) update(username string, record storedLink) error {
	previous, existed := s.links[username]
	s.links[username] = record
	if err := s.save(); err != nil {
		if existed {
			s.links[username] = previous
		} else {
			delete(s.links, username)
		}
		return err
	}
	return nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var file linksFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("reading %s: %w", s.path, err)
	}
	if file.Version != fileVersion {
		return fmt.Errorf("%s has unsupported version %d", s.path, file.Version)
	}
	if file.Links != nil {
		s.links = file.Links
	}
	return nil
}

// save writes the store atomically: a private temp file, fsync, rename, then
// fsync of the directory so the rename itself survives a crash.
func (s *Store) save() error {
	data, err := json.MarshalIndent(linksFile{Version: fileVersion, Links: s.links}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, linksFileName+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	writeErr := errors.Join(tmp.Chmod(0o600), writeAll(tmp, data), tmp.Sync())
	if err := errors.Join(writeErr, tmp.Close()); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	return syncDir(s.dir)
}

func writeAll(f *os.File, data []byte) error {
	_, err := f.Write(data)
	return err
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

func (s *Store) encrypt(username, plaintext string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := s.aead.Seal(nonce, nonce, []byte(plaintext), []byte(username))
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func (s *Store) decrypt(username, encoded string) (string, error) {
	sealed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	nonceSize := s.aead.NonceSize()
	if len(sealed) < nonceSize {
		return "", errors.New("ciphertext too short")
	}
	plaintext, err := s.aead.Open(nil, sealed[:nonceSize], sealed[nonceSize:], []byte(username))
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// lockDir takes an exclusive, non-blocking lock on dir's lock file.
func lockDir(dir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, lockFileName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close() // the lock error is the one worth reporting
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return f, nil
}

// loadOrCreateKey reads the 32-byte key, or creates it (mode 0400) if missing.
func loadOrCreateKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err == nil {
		if len(key) != keySize {
			return nil, fmt.Errorf("%s must contain exactly %d bytes", path, keySize)
		}
		return key, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	key = make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return nil, err
	}
	return key, errors.Join(writeAll(f, key), f.Close())
}
