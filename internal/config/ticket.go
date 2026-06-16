package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// TicketConfig is the [ticket] table in <SESH_HOME>/config.toml — policy knobs for the
// ticket layer (dotfiles-owned, like the rest of config.toml).
type TicketConfig struct {
	// SendPrepend controls whether `ticket send-prompt` prepends the ticket's identity
	// (name + id) to the prompt it delivers, so the agent knows which ticket it is on.
	// nil = the built-in default (ON). A per-call --prepend/--no-prepend flag overrides it.
	SendPrepend *bool `toml:"send_prepend"`
}

type ticketConfigFile struct {
	Ticket *TicketConfig `toml:"ticket"`
}

// LoadTicket reads the [ticket] table. Missing file/table = all defaults. A
// present-but-broken file is a LOUD error (parity with the other config loaders).
func LoadTicket(home string) (TicketConfig, error) {
	raw, err := os.ReadFile(ConfigPath(home))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return TicketConfig{}, nil
		}
		return TicketConfig{}, fmt.Errorf("config: read %s: %w", ConfigPath(home), err)
	}
	var f ticketConfigFile
	if err := toml.Unmarshal(raw, &f); err != nil {
		return TicketConfig{}, fmt.Errorf("config: parse %s: %w", ConfigPath(home), err)
	}
	if f.Ticket == nil {
		return TicketConfig{}, nil
	}
	return *f.Ticket, nil
}

// SendPrependDefault resolves the send-prepend default (unset = true).
func (t TicketConfig) SendPrependDefault() bool {
	if t.SendPrepend == nil {
		return true
	}
	return *t.SendPrepend
}
