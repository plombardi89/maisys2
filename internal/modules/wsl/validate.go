package wsl

import "github.com/plombardi89/maisys2/internal/config"

var _ config.Checker = (*Config)(nil)

// Check checks that the X410 bridge, which wslg = false sets up, has the
// systemd it runs in.
func (c *Config) Check(check *config.Check) {
	if !c.WSLG && !c.Systemd {
		check.Errorf("wslg", "is false, which sets up the X410 bridge as a systemd service, but wsl.systemd is false: turn systemd on, or wslg back on")
	}
}
