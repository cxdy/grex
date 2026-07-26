package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunMissingDatabaseURL(t *testing.T) {
	var out bytes.Buffer
	err := run(context.Background(), "", &out)
	if err == nil {
		t.Fatal("want error for empty DATABASE_URL")
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("error = %q, want it to mention DATABASE_URL", err.Error())
	}
}

func TestRunBadDatabaseURL(t *testing.T) {
	var out bytes.Buffer
	err := run(context.Background(), "not-a-valid-dsn", &out)
	if err == nil {
		t.Fatal("want error for malformed DATABASE_URL")
	}
}
