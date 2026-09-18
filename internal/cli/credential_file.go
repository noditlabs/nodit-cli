package cli

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	keyring "github.com/zalando/go-keyring"
)

const credentialFileVersion byte = 1

var credentialFileAAD = []byte("nodit-cli-credentials-v1")

type fallbackCredentialStore struct {
	primary  credentialStore
	fallback encryptedFileStore
}

func (s fallbackCredentialStore) Get(key string) (string, error) {
	// active() is global: the file may hold one key while others still live in the
	// keyring, so the primary copy goes only after the file answers for this key.
	if s.fallback.active() {
		value, fallbackErr := s.fallback.Get(key)
		if fallbackErr == nil {
			_ = s.primary.Delete(key)
			return value, nil
		}
		if !errors.Is(fallbackErr, keyring.ErrNotFound) {
			return "", fallbackErr
		}
	}
	value, primaryErr := s.primary.Get(key)
	if primaryErr == nil {
		return value, nil
	}
	if errors.Is(primaryErr, keyring.ErrNotFound) {
		return "", keyring.ErrNotFound
	}
	value, fallbackErr := s.fallback.Get(key)
	if errors.Is(fallbackErr, keyring.ErrNotFound) {
		return "", primaryErr
	}
	return value, fallbackErr
}

func (s fallbackCredentialStore) Set(key, value string) error {
	if s.fallback.active() {
		if err := s.fallback.Set(key, value); err != nil {
			return err
		}
		_ = s.primary.Delete(key)
		return nil
	}
	if err := s.primary.Set(key, value); err == nil {
		return nil
	}
	return s.fallback.Set(key, value)
}

func (s fallbackCredentialStore) Delete(key string) error {
	if s.fallback.active() {
		_ = s.primary.Delete(key)
		return s.fallback.Delete(key)
	}
	return s.primary.Delete(key)
}

type encryptedFileStore struct{ dir string }

func (s encryptedFileStore) active() bool {
	_, err := os.Stat(s.dataPath())
	return !errors.Is(err, os.ErrNotExist)
}

func (s encryptedFileStore) Get(key string) (string, error) {
	var value string
	err := s.locked(func() error {
		values, err := s.read()
		if err != nil {
			return err
		}
		var ok bool
		value, ok = values[key]
		if !ok {
			return keyring.ErrNotFound
		}
		return nil
	})
	return value, err
}

func (s encryptedFileStore) Set(key, value string) error {
	return s.locked(func() error {
		values, err := s.read()
		if err != nil {
			return err
		}
		values[key] = value
		return s.write(values)
	})
}

func (s encryptedFileStore) Delete(key string) error {
	return s.locked(func() error {
		values, err := s.read()
		if err != nil {
			return err
		}
		if _, ok := values[key]; !ok {
			return keyring.ErrNotFound
		}
		delete(values, key)
		return s.write(values)
	})
}

func (s encryptedFileStore) locked(fn func() error) error {
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(s.dir, ".credentials.lock"), flock.SetPermissions(0600))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ok, err := lock.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil {
		return err
	}
	if !ok {
		return context.Canceled
	}
	defer lock.Unlock()
	return fn()
}

func (s encryptedFileStore) read() (map[string]string, error) {
	data, err := os.ReadFile(s.dataPath())
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	key, err := s.key(false)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(data) < 1+gcm.NonceSize() || data[0] != credentialFileVersion {
		return nil, errors.New("invalid credential file")
	}
	plaintext, err := gcm.Open(nil, data[1:1+gcm.NonceSize()], data[1+gcm.NonceSize():], credentialFileAAD)
	if err != nil {
		return nil, errors.New("invalid credential file")
	}
	values := map[string]string{}
	if json.Unmarshal(plaintext, &values) != nil {
		return nil, errors.New("invalid credential file")
	}
	return values, nil
}

func (s encryptedFileStore) write(values map[string]string) error {
	key, err := s.key(true)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	plaintext, err := json.Marshal(values)
	if err != nil {
		return err
	}
	data := append([]byte{credentialFileVersion}, nonce...)
	data = gcm.Seal(data, nonce, plaintext, credentialFileAAD)
	return atomicCredentialWrite(s.dataPath(), data)
}

func (s encryptedFileStore) key(create bool) ([]byte, error) {
	key, err := os.ReadFile(s.keyPath())
	if err == nil {
		if len(key) != 32 {
			return nil, errors.New("invalid credential key")
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) || !create {
		return nil, err
	}
	key = make([]byte, 32)
	if _, err = io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(s.keyPath(), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return s.key(false)
	}
	if err != nil {
		return nil, err
	}
	if _, err = file.Write(key); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(s.keyPath())
		return nil, err
	}
	return key, nil
}

func atomicCredentialWrite(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".credentials-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func (s encryptedFileStore) dataPath() string { return filepath.Join(s.dir, "credentials.enc") }
func (s encryptedFileStore) keyPath() string  { return filepath.Join(s.dir, "credentials.key") }
