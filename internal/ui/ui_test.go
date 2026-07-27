package ui

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/protobufs"

	"github.com/dennisme/grex/internal/fleet"
)

func TestUIFleetPage(t *testing.T) {
	r := fleet.New(fleet.Config{
		HeartbeatInterval:     30 * time.Second,
		StaleMissedHeartbeats: 3,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	uid := uuid.New()
	r.Report(&protobufs.AgentToServer{
		InstanceUid: uid[:],
		AgentDescription: &protobufs.AgentDescription{
			IdentifyingAttributes: []*protobufs.KeyValue{{
				Key: "service.name",
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{
					StringValue: "ui-test-agent",
				}},
			}},
		},
		Health: &protobufs.ComponentHealth{Healthy: true},
	}, fleet.ConnMeta{ViaGateway: true, Transport: "ws"})

	h, err := New(r, Config{PollInterval: 5 * time.Second}, time.Now(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mux := http.NewServeMux()
	h.Mount(mux)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"ui-test-agent", "Fleet", "hx-get", "/partials/agents", "Healthy", "Attributes", "attr-chip"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/agents/"+uid.String(), nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /agents/{id} = %d", rec.Code)
	}
	detail := rec.Body.String()
	if !strings.Contains(detail, uid.String()) {
		t.Error("detail page missing instance uid")
	}
	if strings.Contains(detail, `hx-trigger="every`) {
		t.Error("agent detail must not auto-poll (scroll reset)")
	}
	if !strings.Contains(detail, "Refresh") {
		t.Error("agent detail should have a manual Refresh button")
	}

	req = httptest.NewRequest(http.MethodGet, "/status", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Server status") {
		t.Error("status page missing title")
	}

	req = httptest.NewRequest(http.MethodGet, "/static/app.css", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /static/app.css = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/agents/"+uuid.New().String(), nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing agent = %d, want 404", rec.Code)
	}
}

func TestUIFilterQuery(t *testing.T) {
	r := fleet.New(fleet.Config{
		HeartbeatInterval:     30 * time.Second,
		StaleMissedHeartbeats: 3,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	for _, name := range []string{"keep-me", "drop-me"} {
		uid := uuid.New()
		r.Report(&protobufs.AgentToServer{
			InstanceUid: uid[:],
			AgentDescription: &protobufs.AgentDescription{
				IdentifyingAttributes: []*protobufs.KeyValue{{
					Key:   "service.name",
					Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: name}},
				}},
			},
		}, fleet.ConnMeta{})
	}
	h, err := New(r, Config{}, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.Mount(mux)

	req := httptest.NewRequest(http.MethodGet, "/partials/agents?service.name=keep-me", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "keep-me") {
		t.Error("expected keep-me in filtered partial")
	}
	if strings.Contains(body, "drop-me") {
		t.Error("drop-me should be filtered out")
	}
	if !strings.Contains(body, "of 1") {
		t.Errorf("expected total 1 in meta, body=%s", body)
	}
}

func TestNormalizeAttrForm(t *testing.T) {
	// smoke: fleet page with attr form fields
	r := fleet.New(fleet.Config{
		HeartbeatInterval:     30 * time.Second,
		StaleMissedHeartbeats: 3,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	uid := uuid.New()
	r.Report(&protobufs.AgentToServer{
		InstanceUid: uid[:],
		AgentDescription: &protobufs.AgentDescription{
			IdentifyingAttributes: []*protobufs.KeyValue{{
				Key:   "service.name",
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "x"}},
			}},
			NonIdentifyingAttributes: []*protobufs.KeyValue{{
				Key:   "deployment.environment",
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "prod"}},
			}},
		},
	}, fleet.ConnMeta{})
	h, err := New(r, Config{}, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.Mount(mux)
	req := httptest.NewRequest(http.MethodGet, "/?match="+url.QueryEscape("deployment.environment=prod"), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "x") {
		t.Error("expected agent matched by attr matcher")
	}
	if !strings.Contains(rec.Body.String(), "matcher-input") {
		t.Error("expected matcher autocomplete field")
	}
	if !strings.Contains(rec.Body.String(), "filters-labels") {
		t.Error("expected unified filter panel with label section")
	}
}
