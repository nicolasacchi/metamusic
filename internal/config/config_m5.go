package config

// loadM5 reads the close-the-loop settings (C250). Errors name the variable, never a value.
func loadM5(c *Config, getenv func(string) string) error {
	n, err := intVar(getenv, "MM_CALLBACK_BACKLOG_MAX", 6, 0, 50)
	if err != nil {
		return err
	}
	c.CallbackBacklogMax = int(n)
	return nil
}
