package config_test

import (
	"testing"

	"github.com/teovillanueva/code-runner/internal/config"
)

// TestZygoteDefaults asserts the shipped zygote defaults: OFF, sane knobs.
func TestZygoteDefaults(t *testing.T) {
	c := config.Default()
	if c.ZygoteEnabled {
		t.Error("ZygoteEnabled default = true, want false (Docker for everything by default)")
	}
	if c.ZygoteRelayPort != 7000 {
		t.Errorf("ZygoteRelayPort = %d, want 7000", c.ZygoteRelayPort)
	}
	if c.ZygotePoolIdleMs != 300000 {
		t.Errorf("ZygotePoolIdleMs = %d, want 300000", c.ZygotePoolIdleMs)
	}
	if c.ZygoteUIDBase != 100000 {
		t.Errorf("ZygoteUIDBase = %d, want 100000", c.ZygoteUIDBase)
	}
	if c.ZygotePoolMemoryMb != 1024 {
		t.Errorf("ZygotePoolMemoryMb = %d, want 1024", c.ZygotePoolMemoryMb)
	}
}

// TestApplyZygoteEnvParsesAll overlays every knob from a fake env map.
func TestApplyZygoteEnvParsesAll(t *testing.T) {
	env := map[string]string{
		"ZYGOTE_ENABLED":        "true",
		"ZYGOTE_RELAY_PORT":     "9100",
		"ZYGOTE_POOL_IDLE_MS":   "120000",
		"ZYGOTE_UID_BASE":       "500000",
		"ZYGOTE_POOL_MEMORY_MB": "2048",
	}
	get := func(k string) string { return env[k] }

	c := config.Default().ApplyZygoteEnv(get)
	if !c.ZygoteEnabled {
		t.Error("ZygoteEnabled = false, want true")
	}
	if c.ZygoteRelayPort != 9100 {
		t.Errorf("ZygoteRelayPort = %d, want 9100", c.ZygoteRelayPort)
	}
	if c.ZygotePoolIdleMs != 120000 {
		t.Errorf("ZygotePoolIdleMs = %d, want 120000", c.ZygotePoolIdleMs)
	}
	if c.ZygoteUIDBase != 500000 {
		t.Errorf("ZygoteUIDBase = %d, want 500000", c.ZygoteUIDBase)
	}
	if c.ZygotePoolMemoryMb != 2048 {
		t.Errorf("ZygotePoolMemoryMb = %d, want 2048", c.ZygotePoolMemoryMb)
	}
}

// TestApplyZygoteEnvIgnoresUnsetAndBad: unset/invalid vars leave defaults.
func TestApplyZygoteEnvIgnoresUnsetAndBad(t *testing.T) {
	env := map[string]string{
		"ZYGOTE_RELAY_PORT": "not-a-number",
		"ZYGOTE_UID_BASE":   "-5",
	}
	get := func(k string) string { return env[k] }

	base := config.Default()
	c := base.ApplyZygoteEnv(get)
	if c.ZygoteEnabled {
		t.Error("ZygoteEnabled flipped without ZYGOTE_ENABLED set")
	}
	if c.ZygoteRelayPort != base.ZygoteRelayPort {
		t.Errorf("bad ZYGOTE_RELAY_PORT changed value to %d", c.ZygoteRelayPort)
	}
	if c.ZygoteUIDBase != base.ZygoteUIDBase {
		t.Errorf("negative ZYGOTE_UID_BASE changed value to %d", c.ZygoteUIDBase)
	}
}

// TestApplyZygoteEnvEnabledViaOne: ZYGOTE_ENABLED=1 also enables.
func TestApplyZygoteEnvEnabledViaOne(t *testing.T) {
	get := func(k string) string {
		if k == "ZYGOTE_ENABLED" {
			return "1"
		}
		return ""
	}
	if !config.Default().ApplyZygoteEnv(get).ZygoteEnabled {
		t.Error("ZYGOTE_ENABLED=1 did not enable")
	}
}

// TestValidateZygoteOKWhenDisabled: bad zygote knobs are ignored when disabled.
func TestValidateZygoteOKWhenDisabled(t *testing.T) {
	c := config.Default()
	c.ZygoteEnabled = false
	c.ZygoteRelayPort = 0 // would be invalid if enabled
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() with disabled zygote = %v, want nil", err)
	}
}

// TestValidateZygoteEnabledChecksKnobs: enabled + bad port fails fast.
func TestValidateZygoteEnabledChecksKnobs(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*config.Config)
	}{
		{"bad-port-zero", func(c *config.Config) { c.ZygoteRelayPort = 0 }},
		{"bad-port-high", func(c *config.Config) { c.ZygoteRelayPort = 70000 }},
		{"bad-idle", func(c *config.Config) { c.ZygotePoolIdleMs = 0 }},
		{"bad-uid", func(c *config.Config) { c.ZygoteUIDBase = 0 }},
		{"bad-mem", func(c *config.Config) { c.ZygotePoolMemoryMb = 0 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := config.Default()
			c.ZygoteEnabled = true
			tc.mut(&c)
			if err := c.Validate(); err == nil {
				t.Errorf("Validate() = nil, want error for %s", tc.name)
			}
		})
	}
}

// TestValidateZygoteEnabledOK: enabled + sane defaults validates.
func TestValidateZygoteEnabledOK(t *testing.T) {
	c := config.Default()
	c.ZygoteEnabled = true
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() with enabled zygote defaults = %v, want nil", err)
	}
}
