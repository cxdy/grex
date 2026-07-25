package ui

import "testing"

func TestYAMLDisplayExpandsTabs(t *testing.T) {
	t.Parallel()
	in := "receivers:\n\totlp:\n\t\tprotocols: {}"
	got := yamlDisplay(in)
	want := "receivers:\n  otlp:\n    protocols: {}"
	if got != want {
		t.Fatalf("yamlDisplay tabs:\ngot:\n%q\nwant:\n%q", got, want)
	}
}

func TestYAMLDisplayCollapsesFourSpaceIndent(t *testing.T) {
	t.Parallel()
	in := "service:\n    telemetry:\n        metrics:\n            level: Normal\n"
	got := yamlDisplay(in)
	// unit GCD is 4 → scale to 2-space steps
	want := "service:\n  telemetry:\n    metrics:\n      level: Normal\n"
	if got != want {
		t.Fatalf("yamlDisplay 4-space:\ngot:\n%q\nwant:\n%q", got, want)
	}
}

func TestYAMLDisplayLeavesTwoSpaceIndent(t *testing.T) {
	t.Parallel()
	in := "service:\n  telemetry:\n    metrics:\n      level: Normal\n"
	got := yamlDisplay(in)
	if got != in {
		t.Fatalf("yamlDisplay should leave 2-space YAML unchanged:\ngot:\n%q\nwant:\n%q", got, in)
	}
}
