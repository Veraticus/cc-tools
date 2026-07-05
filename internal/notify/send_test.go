package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSend_UrgencyHeaders(t *testing.T) {
	tests := []struct {
		name         string
		urgency      Urgency
		wantPriority string
		wantTags     string
	}{
		{name: "blocked", urgency: UrgencyBlocked, wantPriority: "5", wantTags: "question"},
		{name: "done", urgency: UrgencyDone, wantPriority: "4", wantTags: "white_check_mark"},
		{name: "info", urgency: UrgencyInfo, wantPriority: "3", wantTags: "white_check_mark"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPriority, gotTags, gotTitle, gotBody string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPriority = r.Header.Get("Priority")
				gotTags = r.Header.Get("Tags")
				gotTitle = r.Header.Get("Title")
				body, _ := io.ReadAll(r.Body)
				gotBody = string(body)
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			s := Sender{URL: srv.URL}
			err := s.Send(context.Background(), Notification{Title: "hi", Body: "there", Urgency: tt.urgency})
			if err != nil {
				t.Fatalf("Send() error = %v", err)
			}
			if gotPriority != tt.wantPriority {
				t.Errorf("Priority header = %q, want %q", gotPriority, tt.wantPriority)
			}
			if gotTags != tt.wantTags {
				t.Errorf("Tags header = %q, want %q", gotTags, tt.wantTags)
			}
			if gotTitle != "hi" {
				t.Errorf("Title header = %q, want %q", gotTitle, "hi")
			}
			if gotBody != "there" {
				t.Errorf("body = %q, want %q", gotBody, "there")
			}
		})
	}
}

func TestSend_AuthHeaderPresentWhenTokenSet(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := Sender{URL: srv.URL, Token: "secret-token"}
	if err := s.Send(context.Background(), Notification{Title: "t", Body: "b", Urgency: UrgencyInfo}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer secret-token")
	}
}

func TestSend_AuthHeaderAbsentWhenTokenEmpty(t *testing.T) {
	authSeen := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			authSeen = true
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := Sender{URL: srv.URL}
	if err := s.Send(context.Background(), Notification{Title: "t", Body: "b", Urgency: UrgencyInfo}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if authSeen {
		t.Error("Authorization header present, want absent when Token is empty")
	}
}

func TestSend_RetriesOnceOn500ThenSucceeds(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := Sender{URL: srv.URL}
	if err := s.Send(context.Background(), Notification{Title: "t", Body: "b", Urgency: UrgencyInfo}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if requests != 2 {
		t.Errorf("requests = %d, want 2", requests)
	}
}

func TestSend_403NeverRetries(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	s := Sender{URL: srv.URL}
	err := s.Send(context.Background(), Notification{Title: "t", Body: "b", Urgency: UrgencyInfo})
	if err == nil {
		t.Fatal("Send() error = nil, want error on 403")
	}
	if requests != 1 {
		t.Errorf("requests = %d, want 1", requests)
	}
}

func TestResolveSenderEnv_DirectURL(t *testing.T) {
	s, ok := ResolveSenderEnv([]string{"CLAUDE_HOOKS_NTFY_URL=https://ntfy.sh/mytopic"})
	if !ok {
		t.Fatal("ResolveSenderEnv() ok = false, want true")
	}
	if s.URL != "https://ntfy.sh/mytopic" {
		t.Errorf("URL = %q, want %q", s.URL, "https://ntfy.sh/mytopic")
	}
}

func TestResolveSenderEnv_URLFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "url")
	if err := os.WriteFile(path, []byte("https://ntfy.sh/fromfile\n"), 0o644); err != nil {
		t.Fatalf("writing url file: %v", err)
	}

	s, ok := ResolveSenderEnv([]string{"CLAUDE_HOOKS_NTFY_URL_FILE=" + path})
	if !ok {
		t.Fatal("ResolveSenderEnv() ok = false, want true")
	}
	if s.URL != "https://ntfy.sh/fromfile" {
		t.Errorf("URL = %q, want %q", s.URL, "https://ntfy.sh/fromfile")
	}
}

func TestResolveSenderEnv_MissingFileFallsThroughToNotOK(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	_, ok := ResolveSenderEnv([]string{"CLAUDE_HOOKS_NTFY_URL_FILE=" + missing})
	if ok {
		t.Fatal("ResolveSenderEnv() ok = true, want false when URL_FILE is missing and no direct URL")
	}
}

func TestResolveSenderEnv_Disabled(t *testing.T) {
	_, ok := ResolveSenderEnv([]string{
		"CLAUDE_HOOKS_NTFY_URL=https://ntfy.sh/mytopic",
		"CLAUDE_HOOKS_NTFY_DISABLED=true",
	})
	if ok {
		t.Fatal("ResolveSenderEnv() ok = true, want false when DISABLED=true")
	}
}

func TestResolveSenderEnv_TokenFileTrimmed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("  s3cret\n\n"), 0o644); err != nil {
		t.Fatalf("writing token file: %v", err)
	}

	s, ok := ResolveSenderEnv([]string{
		"CLAUDE_HOOKS_NTFY_URL=https://ntfy.sh/mytopic",
		"CLAUDE_HOOKS_NTFY_TOKEN_FILE=" + path,
	})
	if !ok {
		t.Fatal("ResolveSenderEnv() ok = false, want true")
	}
	if s.Token != "s3cret" {
		t.Errorf("Token = %q, want %q", s.Token, "s3cret")
	}
	if strings.ContainsAny(s.Token, " \n") {
		t.Errorf("Token = %q, want no whitespace", s.Token)
	}
}
