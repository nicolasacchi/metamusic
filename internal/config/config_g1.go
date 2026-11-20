package config

import (
	"errors"
	"time"
)

// loadG1 reads the G1 settings (C324.2): MM_TZ, an IANA zone name for scheduled work. Empty means the process zone. Errors name
// the variable, never the value.
func loadG1(c *Config, getenv func(string) string) error {
	v := getenv("MM_TZ")
	if v == "" {
		return nil
	}
	if v == "Local" { // time.LoadLocation accepts it; the process zone is spelled by leaving the variable empty
		return errors.New("config: MM_TZ must be an IANA zone name such as Europe/Rome, or empty for the process zone")
	}
	loc, err := time.LoadLocation(v)
	if err != nil {
		return errors.New("config: MM_TZ must be an IANA zone name such as Europe/Rome, or empty for the process zone")
	}
	c.Loc = loc
	return nil
}

// Zone is the zone scheduled work runs in: MM_TZ, else the process zone.
func (c Config) Zone() *time.Location {
	if c.Loc == nil {
		return time.Local
	}
	return c.Loc
}
