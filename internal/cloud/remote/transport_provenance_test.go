package remote

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMutationProvenanceRequests(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		call             func(*MutationTransport) error
	}{
		{"register", "/sync/session-authorities", `{"session_id":"session","project":"owner"}`, func(mt *MutationTransport) error { return mt.RegisterSessionAuthority("session", "owner") }},
		{"claim", "/sync/prompt-pair-claims", `{"session_id":"session","source_inbox_id":"inbox","sync_id":"sync","owner_project":"owner","project":"prompt"}`, func(mt *MutationTransport) error {
			return mt.ClaimPromptPair("session", "inbox", "sync", "owner", "prompt")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, _ := io.ReadAll(r.Body)
				if r.Method != http.MethodPost || r.URL.Path != tc.path || r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("Content-Type") != "application/json" || string(data) != tc.body {
					t.Errorf("request method=%s path=%s auth=%q content=%q body=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), data)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			}))
			defer server.Close()
			mt, err := NewMutationTransport(server.URL, "secret")
			if err != nil {
				t.Fatal(err)
			}
			mt.httpClient = server.Client()
			if err := tc.call(mt); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMutationProvenanceStatus(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body, code string
	}{
		{"unauthorized", 401, `{"error":"denied"}`, ""}, {"forbidden", 403, `{"error":"denied"}`, ""},
		{"conflict", 409, `{"error":"conflict"}`, ""}, {"oversize", 413, `{"error":"too large"}`, ""},
		{"authority unavailable", 404, `{"error":"session authority unavailable","error_code":"session_authority_unavailable"}`, "session_authority_unavailable"},
		{"old server", 404, "404 page not found", "server_unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			mt, err := NewMutationTransport(server.URL, "")
			if err != nil {
				t.Fatal(err)
			}
			for i, call := range []func() error{func() error { return mt.RegisterSessionAuthority("s", "owner") }, func() error { return mt.ClaimPromptPair("s", "i", "y", "owner", "prompt") }} {
				var status *HTTPStatusError
				wantCode := tc.code
				if i == 0 && tc.status == 404 {
					wantCode = "server_unsupported"
				}
				if err := call(); !errors.As(err, &status) || status.StatusCode != tc.status || status.ErrorCode != wantCode {
					t.Fatalf("error=%v status=%+v", err, status)
				}
			}
		})
	}
}

func TestMutationProvenanceRejectsUnverifiedSuccess(t *testing.T) {
	for _, body := range []string{"", `{"status":"pending"}`, `<html>login</html>`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			mt := mustNewMutationTransport(t, server.URL, "")
			if err := mt.ClaimPromptPair("s", "i", "y", "owner", "prompt"); err == nil {
				t.Fatal("accepted response without confirmed claim")
			}
		})
	}
}

func TestMutationProvenanceNetworkFailure(t *testing.T) {
	mt, err := NewMutationTransport("http://cloud.example.test", "")
	if err != nil {
		t.Fatal(err)
	}
	mt.httpClient = &http.Client{Transport: remoteRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("offline")
	})}
	if err := mt.ClaimPromptPair("s", "i", "y", "owner", "prompt"); err == nil {
		t.Fatal("network failure accepted")
	}
	if _, err := NewMutationTransport("http://example.com", "secret"); err == nil {
		t.Fatal("insecure bearer accepted")
	}
}
