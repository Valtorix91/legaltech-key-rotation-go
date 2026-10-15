package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestPlanFollowUp(t *testing.T) {
	now := time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		in   MatterIntake
		want FollowUpPlan
	}{
		{"unsigned matter holds", MatterIntake{MatterID: "MAT-10", Deadline: now.Add(48 * time.Hour)}, FollowUpPlan{"MAT-10", "awaiting_signature", "hold"}},
		{"signed upcoming matter schedules follow-up", MatterIntake{MatterID: "MAT-11", SignedDocumentURL: "https://docs.example/signed", Deadline: now.Add(48 * time.Hour)}, FollowUpPlan{"MAT-11", "signed", "schedule_deadline_follow_up"}},
		{"signed overdue matter escalates", MatterIntake{MatterID: "MAT-12", SignedDocumentURL: "https://docs.example/signed", Deadline: now.Add(-time.Hour)}, FollowUpPlan{"MAT-12", "deadline_passed", "escalate"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := planFollowUp(tt.in, now); got != tt.want {
				t.Fatalf("planFollowUp() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRotateAuditAndCleanupRevokesTemporaryKey(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case "/v1/account/keys/create":
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]any{"key_id": "key/123", "key_secret": "secret"}})
		case "/v1/account/keys/rotate/key%2F123":
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]any{"key_id": "ifr_abc...", "key": "ifr_secret5678"}})
		case "/v1/logs/search":
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]any{"entries": []any{}}})
		case "/v1/account/keys/revoke/ifr_...5678":
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]any{}})
		case "/v1/account/keys/revoke/key%2F123":
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient("test-key", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = client.RotateAuditAndCleanup(context.Background(), 24)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"POST /v1/account/keys/create",
		"POST /v1/account/keys/rotate/key%2F123",
		"GET /v1/logs/search?q=key%2F123",
		"DELETE /v1/account/keys/revoke/ifr_...5678",
		"DELETE /v1/account/keys/revoke/key%2F123",
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests = %#v, want %#v", requests, want)
	}
}
