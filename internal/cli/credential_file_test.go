package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"testing"

	keyring "github.com/zalando/go-keyring"
)

func TestEncryptedFileStoreRoundTripAndTamperDetection(t *testing.T) {
	store := encryptedFileStore{t.TempDir()}
	secret := "sensitive-file-fallback-value"
	if err := store.Set("oauth-session", secret); err != nil {
		t.Fatal(err)
	}
	value, err := store.Get("oauth-session")
	if err != nil || value != secret {
		t.Fatalf("%q %v", value, err)
	}
	data, err := os.ReadFile(store.dataPath())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(secret)) {
		t.Fatal("credential file contains plaintext")
	}
	if runtime.GOOS != "windows" {
		for _, path := range []string{store.dataPath(), store.keyPath()} {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("%s permissions: %v %v", path, info, err)
			}
		}
	}
	data[len(data)-1] ^= 1
	if err := os.WriteFile(store.dataPath(), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("oauth-session"); err == nil {
		t.Fatal("tampered credential file was accepted")
	}
}

func TestEncryptedFileStoreConcurrentUpdates(t *testing.T) {
	store := encryptedFileStore{t.TempDir()}
	var group sync.WaitGroup
	errorsFound := make(chan error, 10)
	for i := range 10 {
		group.Add(1)
		go func() {
			defer group.Done()
			errorsFound <- store.Set(fmt.Sprintf("key-%d", i), fmt.Sprintf("value-%d", i))
		}()
	}
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := range 10 {
		value, err := store.Get(fmt.Sprintf("key-%d", i))
		if err != nil || value != fmt.Sprintf("value-%d", i) {
			t.Fatalf("key-%d: %q %v", i, value, err)
		}
	}
}

func TestCredentialStoreFallsBackToEncryptedFile(t *testing.T) {
	primary := &fakeKeys{values: map[string]string{}, err: errors.New("keychain unavailable")}
	file := encryptedFileStore{t.TempDir()}
	store := fallbackCredentialStore{primary: primary, fallback: file}
	if err := store.Set("test-credential", "fallback-secret"); err != nil {
		t.Fatal(err)
	}
	value, err := store.Get("test-credential")
	if err != nil || value != "fallback-secret" {
		t.Fatalf("%q %v", value, err)
	}
	if err := store.Delete("test-credential"); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Get("test-credential"); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("fallback credential remains: %v", err)
	}
}

func TestCredentialStoreKeepsUsingActivatedFallback(t *testing.T) {
	primary := &fakeKeys{values: map[string]string{}, err: errors.New("keychain unavailable")}
	file := encryptedFileStore{t.TempDir()}
	store := fallbackCredentialStore{primary: primary, fallback: file}
	if err := store.Set("test-credential", "fallback-secret"); err != nil {
		t.Fatal(err)
	}
	primary.err = nil
	primary.values["test-credential"] = "stale-primary-secret"
	value, err := store.Get("test-credential")
	if err != nil || value != "fallback-secret" {
		t.Fatalf("%q %v", value, err)
	}
	if _, ok := primary.values["test-credential"]; ok {
		t.Fatal("stale OS credential was not removed")
	}
}

func TestCredentialStoreReadsAnUnusedFailingKeyringAsEmpty(t *testing.T) {
	// A server with no Secret Service fails every keyring call. auth status and the login check
	// read before anything is written.
	primary := &fakeKeys{values: map[string]string{}, err: errors.New("no session bus")}
	store := fallbackCredentialStore{primary: primary, fallback: encryptedFileStore{t.TempDir()}}
	if _, err := store.Get(sessionKey); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("read before login: %v", err)
	}
	// A missing mark does not prove the keyring empty, so a delete there is still reported.
	if err := store.Delete(sessionKey); err == nil || errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("logout before login: %v", err)
	}
	if err := store.Set(sessionKey, "session-value"); err != nil {
		t.Fatal(err)
	}
	if value, err := store.Get(sessionKey); err != nil || value != "session-value" {
		t.Fatalf("%q %v", value, err)
	}
	if err := store.Delete(sessionKey); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(sessionKey); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("read after logout: %v", err)
	}
}

func TestCredentialStoreReportsAFailingKeyringOnceItHeldAValue(t *testing.T) {
	for name, use := range map[string]func(credentialStore) error{
		"written": func(s credentialStore) error { return s.Set(sessionKey, "session-value") },
		// A keyring filled by an earlier version is recognised on its first successful read.
		"read": func(s credentialStore) error { _, err := s.Get(sessionKey); return err },
	} {
		primary := &fakeKeys{values: map[string]string{sessionKey: "session-value"}}
		store := fallbackCredentialStore{primary: primary, fallback: encryptedFileStore{t.TempDir()}}
		if err := use(store); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		// A locked keychain or a closed password manager hides the value without removing it.
		primary.err = errors.New("keychain locked")
		if _, err := store.Get(sessionKey); err == nil || errors.Is(err, keyring.ErrNotFound) {
			t.Fatalf("%s: read from a locked keyring: %v", name, err)
		}
		if err := store.Delete(sessionKey); err == nil || errors.Is(err, keyring.ErrNotFound) {
			t.Fatalf("%s: delete from a locked keyring: %v", name, err)
		}
	}
}

func TestCredentialStorePrefersOSStore(t *testing.T) {
	primary := &fakeKeys{values: map[string]string{}}
	file := encryptedFileStore{t.TempDir()}
	store := fallbackCredentialStore{primary: primary, fallback: file}
	if err := store.Set("test-credential", "primary-secret"); err != nil {
		t.Fatal(err)
	}
	value, err := store.Get("test-credential")
	if err != nil || value != "primary-secret" {
		t.Fatalf("%q %v", value, err)
	}
	if _, err := os.Stat(file.dataPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fallback file created while OS store was available: %v", err)
	}
}

func TestFallbackStoreKeepsKeyringEntriesItCannotServe(t *testing.T) {
	primary := &fakeKeys{values: map[string]string{"project-key": "keyring-only-value"}}
	store := fallbackCredentialStore{primary: primary, fallback: encryptedFileStore{t.TempDir()}}

	// A locked keychain sends one key to the file, which activates it for every
	// key, so a key the file never held must still come back from the keyring.
	if err := store.fallback.Set("oauth-session", "session-value"); err != nil {
		t.Fatal(err)
	}
	if !store.fallback.active() {
		t.Fatal("fallback did not activate")
	}
	for i := 0; i < 2; i++ {
		value, err := store.Get("project-key")
		if err != nil || value != "keyring-only-value" {
			t.Fatalf("read %d: %q %v", i, value, err)
		}
	}
	value, err := store.Get("oauth-session")
	if err != nil || value != "session-value" {
		t.Fatalf("%q %v", value, err)
	}
	if _, err := primary.Get("oauth-session"); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("keyring copy of a file-backed key survived: %v", err)
	}
}

func TestFallbackStoreKeepsPrimaryWhenFileWriteFails(t *testing.T) {
	primary := &fakeKeys{values: map[string]string{"project-key": "keyring-only-value"}}
	dir := t.TempDir()
	store := fallbackCredentialStore{primary: primary, fallback: encryptedFileStore{dir}}
	if err := store.fallback.Set("oauth-session", "session-value"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Skip("cannot make the credential directory read-only")
	}
	defer os.Chmod(dir, 0700)
	if err := store.Set("project-key", "new-value"); err == nil {
		t.Skip("the read-only directory still accepted a write")
	}
	if value, err := primary.Get("project-key"); err != nil || value != "keyring-only-value" {
		t.Fatalf("keyring copy was dropped after a failed file write: %q %v", value, err)
	}
}
