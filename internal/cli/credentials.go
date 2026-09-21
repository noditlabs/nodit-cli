package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	keyring "github.com/zalando/go-keyring"
)

type credentialStore interface {
	Get(string) (string, error)
	Set(string, string) error
	Delete(string) error
}

type systemKeyring struct{ service string }

func (s systemKeyring) Get(key string) (string, error) { return keyring.Get(s.service, key) }
func (s systemKeyring) Set(key, value string) error    { return keyring.Set(s.service, key, value) }
func (s systemKeyring) Delete(key string) error        { return keyring.Delete(s.service, key) }

const sessionKey = "oauth-session"
const projectAPIKeyPrefix = "project-api-key:"

func (a *app) apiKey(explicit string) (string, error) {
	key := explicit
	if key == "" {
		key = a.getenv("NODIT_API_KEY")
	}
	if key == "" {
		c, err := a.config.read()
		if err != nil {
			return "", err
		}
		if c.Project != "" {
			keyID := c.ProjectKeys[c.Project]
			if keyID == "" {
				return "", failure(
					"PROJECT_API_KEY_REQUIRED",
					"The selected project has no linked API key. Run nodit project select "+c.Project+".",
				)
			}
			key, err = a.keys.Get(projectCredentialKey(c.Project, keyID))
			if errors.Is(err, keyring.ErrNotFound) {
				return "", failure(
					"PROJECT_API_KEY_REQUIRED",
					"The selected project's API key is missing from the credential store. "+
						"Run nodit project select "+c.Project+" to link it again.",
				)
			}
			if err != nil {
				return "", storageError()
			}
		}
	}
	if key == "" {
		return "", failure(
			"API_KEY_REQUIRED",
			"An API key is required. Set NODIT_API_KEY, or run nodit auth login and "+
				"nodit project select <project-id> to link one.",
		)
	}
	if strings.IndexFunc(key, func(r rune) bool { return r <= ' ' || r >= 127 }) >= 0 {
		return "", invalid("API key must contain only printable ASCII characters without whitespace.")
	}
	return key, nil
}

func projectCredentialKey(projectID, keyID string) string {
	return projectAPIKeyPrefix + projectID + ":" + keyID
}

type session struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken,omitempty"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Issuer       string    `json:"issuer"`
	Resource     string    `json:"resource"`
}

func (a *app) loadSession() (*session, error) {
	raw, err := a.keys.Get(sessionKey)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, storageError()
	}
	var s session
	if json.Unmarshal([]byte(raw), &s) != nil || s.AccessToken == "" || s.ExpiresAt.IsZero() || s.Issuer != a.env.Issuer || s.Resource != a.env.Resource {
		return nil, failure("INVALID_SESSION", "Saved login is invalid for this build. Run nodit auth login.")
	}
	return &s, nil
}

func (a *app) saveSession(s *session) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if a.keys.Set(sessionKey, string(b)) != nil {
		return storageError()
	}
	return nil
}

func (a *app) deleteCredential(key string) error {
	err := a.keys.Delete(key)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return storageError()
	}
	return nil
}

// The lock protects rotated refresh tokens across concurrent CLI processes.
func (a *app) accessToken(ctx context.Context) (string, error) {
	if external := a.getenv("NODIT_AUTH_TOKEN"); external != "" {
		return external, nil
	}
	var token string
	err := a.config.locked(ctx, func() error {
		s, err := a.loadSession()
		if err != nil {
			return err
		}
		if s == nil {
			return failure("AUTH_REQUIRED", "Login is required. Run nodit auth login.")
		}
		if s.ExpiresAt.After(a.now().Add(30 * time.Second)) {
			token = s.AccessToken
			return nil
		}
		if s.RefreshToken == "" {
			return failure("AUTH_EXPIRED", "Saved login has expired. Run nodit auth login.")
		}
		m, err := a.discover(ctx)
		if err != nil {
			return err
		}
		next, err := a.exchange(ctx, m.TokenEndpoint, mapValues(
			"grant_type", "refresh_token", "refresh_token", s.RefreshToken,
		))
		if err != nil {
			return err
		}
		if next.RefreshToken == "" {
			return failure("INVALID_TOKEN_RESPONSE", "The server did not return a rotated refresh token.")
		}
		if err = a.saveSession(next); err != nil {
			return err
		}
		token = next.AccessToken
		return nil
	})
	return token, err
}
