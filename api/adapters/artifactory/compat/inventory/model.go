package main

import (
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Inventory is the schema of compat/docs/commands.yaml.
type Inventory struct {
	JFVersion string    `yaml:"jf_version"`
	Commands  []Command `yaml:"commands"`
}

// Command is one client command.
type Command struct {
	Command   string   `yaml:"command"`
	Aliases   []string `yaml:"aliases,omitempty"`
	Hidden    bool     `yaml:"hidden,omitempty"`
	Tier      string   `yaml:"tier"`
	Category  string   `yaml:"category"`
	LocalOnly bool     `yaml:"local_only"`
	Flags     []string `yaml:"flags,omitempty"`
}

// Rules is the schema of compat/inventory/tiers.yaml.
type Rules struct {
	HiddenCandidates []string `yaml:"hidden_candidates"`
	Rules            []Rule   `yaml:"rules"`
}

// Rule assigns tier metadata.
type Rule struct {
	Match     []string `yaml:"match"`
	Tier      string   `yaml:"tier,omitempty"`
	Category  string   `yaml:"category,omitempty"`
	LocalOnly *bool    `yaml:"local_only,omitempty"`
}

func (r Rule) matches(path string) bool {
	for _, m := range r.Match {
		if prefix, ok := strings.CutSuffix(m, " *"); ok {
			if path == prefix || strings.HasPrefix(path, prefix+" ") {
				return true
			}
		} else if suffix, ok := strings.CutPrefix(m, "*"); ok && suffix != "" {
			if !strings.Contains(path, " ") && strings.HasSuffix(path, suffix) {
				return true
			}
		} else if m == "*" || m == path {
			return true
		}
	}

	return false
}

func classify(c *Command, rules []Rule) {
	localSet := false

	for _, r := range rules {
		if !r.matches(c.Command) {
			continue
		}

		if c.Tier == "" {
			c.Tier = r.Tier
		}

		if c.Category == "" {
			c.Category = r.Category
		}

		if !localSet && r.LocalOnly != nil {
			c.LocalOnly, localSet = *r.LocalOnly, true
		}
	}

	if c.Tier == "" {
		c.Tier = "unassigned"
	}
}

func readYAML(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	if err := yaml.Unmarshal(b, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	return nil
}

func readInventory(path string) (Inventory, error) {
	var inv Inventory
	if err := readYAML(path, &inv); err != nil {
		return Inventory{}, err
	}

	return inv, nil
}

func writeYAML(path, header string, v any) error {
	b, err := yaml.Marshal(v)
	if err != nil {
		return err
	}

	return os.WriteFile(path, append([]byte(header), b...), 0o600)
}
