package webhook

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPostJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want %q", r.Method, http.MethodPost)
		}
		if contentType := r.Header.Get("Content-Type"); contentType != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", contentType)
		}

		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
			return
		}
		if payload["ok"] != true || payload["session_id"] != "session-1" {
			t.Errorf("unexpected payload: %#v", payload)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	if err := PostJSON(server.URL, map[string]interface{}{
		"ok":         true,
		"session_id": "session-1",
	}); err != nil {
		t.Fatalf("PostJSON() error = %v", err)
	}
}
