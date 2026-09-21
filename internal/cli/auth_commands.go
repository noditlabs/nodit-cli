package cli

import (
	"github.com/spf13/cobra"
)

func (a *app) authCommand() *cobra.Command {
	r := asGroup(&cobra.Command{Use: "auth", Short: "Manage OAuth login"})
	r.AddCommand(&cobra.Command{Use: "login", Short: "Log in using browser OAuth with PKCE", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return a.login(cmd.Context()) }})
	r.AddCommand(&cobra.Command{Use: "status", Short: "Show local credential sources without revealing secrets", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		login := map[string]any{"source": "none", "status": "not_logged_in"}
		if a.getenv("NODIT_AUTH_TOKEN") != "" {
			login = map[string]any{"source": "NODIT_AUTH_TOKEN", "status": "unverified"}
		} else {
			s, err := a.loadSession()
			if err != nil {
				return err
			}
			if s != nil {
				state := "valid_locally"
				if !s.ExpiresAt.After(a.now()) {
					state = "expired"
				}
				login = map[string]any{"source": "credential_store", "status": state, "expiresAt": s.ExpiresAt, "refreshable": s.RefreshToken != ""}
			}
		}
		keySource := "none"
		var keyProject, keyID string
		if a.getenv("NODIT_API_KEY") != "" {
			keySource = "NODIT_API_KEY"
		} else {
			c, err := a.config.read()
			if err != nil {
				return err
			}
			if c.Project != "" {
				keyProject, keyID = c.Project, c.ProjectKeys[c.Project]
				if keyID != "" {
					present, err := a.credentialPresent(projectCredentialKey(keyProject, keyID))
					if err != nil {
						return err
					}
					if present {
						keySource = "project_credential_store"
					}
				}
			}
		}
		apiKeyStatus := map[string]any{"source": keySource, "configured": keySource != "none"}
		if keyProject != "" {
			apiKeyStatus["projectId"] = keyProject
			apiKeyStatus["keyId"] = keyID
		}
		return a.success(map[string]any{"oauth": login, "apiKey": apiKeyStatus})
	}})
	r.AddCommand(&cobra.Command{Use: "logout", Short: "Remove OAuth login and its project-linked keys", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		linked := []string{sessionKey}
		if err := a.config.update(cmd.Context(), func(c *config) error {
			for project, keyID := range c.ProjectKeys {
				linked = append(linked, projectCredentialKey(project, keyID))
			}
			c.Project = ""
			c.ProjectKeys = nil
			return nil
		}); err != nil {
			return err
		}
		// Deleting after the write keeps a failed config update from stranding the
		// config on credentials that are already gone.
		for _, key := range linked {
			if err := a.deleteCredential(key); err != nil {
				return err
			}
		}
		return a.success(map[string]any{"savedLoginRemoved": true, "projectKeysRemoved": true, "externalTokenActive": a.getenv("NODIT_AUTH_TOKEN") != ""})
	}})
	return r
}
