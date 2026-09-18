package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

type config struct {
	Network     string            `json:"network,omitempty"`
	Output      string            `json:"output,omitempty"`
	Project     string            `json:"project,omitempty"`
	ProjectKeys map[string]string `json:"projectKeys,omitempty"`
}

type configStore struct{ dir string }

func (s configStore) path() string { return filepath.Join(s.dir, "config.json") }

func (s configStore) read() (config, error) {
	var c config
	b, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, failure("CONFIG_READ_FAILED", "Cannot read config.")
	}
	if json.Unmarshal(b, &c) != nil {
		return c, failure("INVALID_CONFIG", "Config is not valid JSON.")
	}
	return c, nil
}

func (s configStore) locked(ctx context.Context, fn func() error) error {
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return failure("CONFIG_WRITE_FAILED", "Cannot create config directory.")
	}
	l := flock.New(filepath.Join(s.dir, ".lock"), flock.SetPermissions(0600))
	lockCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ok, err := l.TryLockContext(lockCtx, 25*time.Millisecond)
	if err != nil || !ok {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return failure("CONFIG_LOCK_FAILED", "Cannot lock local state.")
	}
	defer l.Unlock()
	return fn()
}

func (s configStore) update(ctx context.Context, change func(*config) error) error {
	return s.locked(ctx, func() error {
		c, err := s.read()
		if err != nil {
			return err
		}
		if err = change(&c); err != nil {
			return err
		}
		b, err := json.MarshalIndent(c, "", "  ")
		if err != nil {
			return err
		}
		f, err := os.CreateTemp(s.dir, ".config-*")
		if err != nil {
			return failure("CONFIG_WRITE_FAILED", "Cannot save config.")
		}
		defer os.Remove(f.Name())
		defer f.Close()
		if _, err = f.Write(append(b, '\n')); err == nil {
			err = f.Sync()
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(f.Name(), s.path())
		}
		if err != nil {
			return failure("CONFIG_WRITE_FAILED", "Cannot save config.")
		}
		return nil
	})
}

func resolveNetwork(flag, env string, c config) (string, error) {
	id := flag
	if id == "" {
		id = env
	}
	if id == "" {
		id = c.Network
	}
	if id == "" {
		return "", invalid("A network is required. Use --network or nodit config set network <id>.")
	}
	_, err := findNetwork(id)
	return id, err
}
