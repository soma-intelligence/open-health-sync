// SPDX-License-Identifier: AGPL-3.0-only

package settings

import (
	"context"
	"fmt"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/db"
)

// Start is what a process learned about the stored settings when it started.
type Start struct {
	// Revision is the settings revision the configuration was resolved at.
	Revision int64
	// Invalid are the stored values skipped, Unknown the stored names the
	// registry does not know. A heartbeat reports both.
	Invalid []Invalid
	Unknown []string
}

const openErrMessage = "the stored value does not open with OHS_ENCRYPTION_KEY or OHS_PREVIOUS_ENCRYPTION_KEYS: save it again"

// Boot reads the stored settings with the database and key of envCfg (the
// configuration the environment alone gives) and returns the configuration
// with them stacked under the environment. A stored value that does not
// hold is skipped and logged by name, never by value; only a failed read
// stops the start.
func Boot(ctx context.Context, envCfg config.Config, env func(string) (string, bool), environ []string) (config.Config, Start, error) {
	box, err := crypto.NewKeyring(envCfg.EncryptionKey, envCfg.PreviousEncryptionKeys...)
	if err != nil {
		return config.Config{}, Start{}, fmt.Errorf("settings: %w", err)
	}
	pool, err := db.Open(ctx, envCfg.DatabaseURL)
	if err != nil {
		return config.Config{}, Start{}, fmt.Errorf("settings: %w", err)
	}
	st, err := NewPG(pool, box).Load(ctx)
	pool.Close()
	if err != nil {
		return config.Config{}, Start{}, err
	}

	stored := make(map[string]string, len(st.Rows))
	var sealedInvalid []Invalid
	for name, r := range st.Rows {
		if r.OpenErr != nil {
			if s, ok := config.LookupSetting(name); ok && s.Apply != config.ApplyBootstrap && !config.EnvSet(env, name) {
				sealedInvalid = append(sealedInvalid, Invalid{Name: name, Message: openErrMessage})
			}
			continue
		}
		stored[name] = r.Value
	}
	cfg, invalid, unknown, err := Resolve(env, environ, stored)
	if err != nil {
		return config.Config{}, Start{}, err
	}
	invalid = append(invalid, sealedInvalid...)
	for _, inv := range invalid {
		modlog().Warn().Str("setting", inv.Name).Str("error", inv.Message).Msg("stored_setting_skipped")
	}
	for _, n := range unknown {
		modlog().Warn().Str("setting", n).Msg("stored_setting_unknown_ignored")
	}
	return cfg, Start{Revision: st.Revision, Invalid: invalid, Unknown: unknown}, nil
}
