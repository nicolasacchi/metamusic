package config

import "errors"

// loadStepUp reads MM_STEPUP_OIDC_CLIENT (proxy-mode step-up through the OIDC issuer). Unset leaves the feature off.
// Errors name the variable, never a value.
func loadStepUp(c *Config, getenv func(string) string) error {
	id := getenv("MM_STEPUP_OIDC_CLIENT")
	if id == "" {
		return nil
	}
	if !oidcClientIDRE.MatchString(id) {
		return errors.New(`config: MM_STEPUP_OIDC_CLIENT must match ^[A-Za-z0-9._~-]{1,100}$`)
	}
	if c.AuthMode != "proxy" {
		return errors.New("config: MM_STEPUP_OIDC_CLIENT only applies to MM_AUTH=proxy (local mode has its own password step-up)")
	}
	if c.OIDCIssuer == "" {
		return errors.New("config: MM_STEPUP_OIDC_CLIENT needs MM_OIDC_ISSUER (the issuer the step-up signs in with)")
	}
	c.StepUpClientID = id
	return nil
}
