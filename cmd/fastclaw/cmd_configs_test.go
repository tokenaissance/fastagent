package main

import "testing"

func TestConfigsReconcileMirrorCmd_Structure(t *testing.T) {
	root := configsCmd()
	if root.Use != "configs" {
		t.Errorf("expected Use='configs', got %q", root.Use)
	}

	var found bool
	for _, sub := range root.Commands() {
		if sub.Use != "reconcile-mirror" {
			continue
		}
		found = true
		strict := sub.Flags().Lookup("strict")
		if strict == nil {
			t.Fatal("missing --strict flag")
		}
		if strict.DefValue != "false" {
			t.Errorf("--strict default = %q, want false", strict.DefValue)
		}
	}
	if !found {
		t.Fatal("missing reconcile-mirror subcommand")
	}
}
