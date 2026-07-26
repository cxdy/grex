package main

import (
	"context"
	"strings"
	"testing"
)

func TestRunMissingDatabaseURL(t *testing.T) {
	err := run(context.Background(), "", nil)
	if err == nil {
		t.Fatal("want error for empty DATABASE_URL")
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("error = %q, want it to mention DATABASE_URL", err.Error())
	}
}

func TestRunBadDatabaseURL(t *testing.T) {
	err := run(context.Background(), "not-a-valid-dsn", nil)
	if err == nil {
		t.Fatal("want error for malformed DATABASE_URL")
	}
}
