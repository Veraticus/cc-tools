package notify

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// defaultSenderTimeout bounds the HTTP client Send builds when Sender.Client
// is nil.
const defaultSenderTimeout = 5 * time.Second

// senderRetryDelay is how long Send waits before its single retry attempt on
// a transport error or 5xx response.
const senderRetryDelay = 1 * time.Second

// Sender posts notifications to an ntfy topic.
type Sender struct {
	URL   string
	Token string
	// Client is the HTTP client Send uses. A nil Client gets a
	// defaultSenderTimeout-bounded http.Client at Send time.
	Client *http.Client
}

// Notification is one message to deliver.
type Notification struct {
	Title   string
	Body    string
	Urgency Urgency
}

// tagCheckMark is the ntfy tag shared by UrgencyDone and UrgencyInfo: both
// chime the same "finished" sound, differing only in Priority (phone buzz
// strength).
const tagCheckMark = "white_check_mark"

// ntfyHeaders maps an Urgency to its ntfy Priority/Tags header pair. This is
// the gnomon-chime contract: a subscriber there picks its sound by the
// `question` tag, and the phone's buzz strength follows Priority.
// UrgencyBlocked wakes the user for something gating progress; UrgencyDone
// and UrgencyInfo both chime the same "finished" sound, differing only in
// phone buzz strength.
func ntfyHeaders(u Urgency) (string, string) {
	switch u {
	case UrgencyBlocked:
		return "5", "question"
	case UrgencyDone:
		return "4", tagCheckMark
	case UrgencyInfo:
		return "3", tagCheckMark
	default:
		return "3", tagCheckMark
	}
}

// Send posts n to the ntfy topic. It retries exactly once, after
// senderRetryDelay, on a transport error or a 5xx response — a 4xx is a
// config error (bad topic, bad token) that retrying would only spam, so it
// is returned immediately with no retry.
func (s Sender) Send(ctx context.Context, n Notification) error {
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: defaultSenderTimeout}
	}
	priority, tags := ntfyHeaders(n.Urgency)

	retry, err := s.post(ctx, client, n, priority, tags)
	if err == nil || !retry {
		return err
	}

	select {
	case <-time.After(senderRetryDelay):
	case <-ctx.Done():
		return fmt.Errorf("notify: waiting to retry: %w", ctx.Err())
	}

	_, err = s.post(ctx, client, n, priority, tags)
	return err
}

// post makes one attempt against the ntfy topic and reports whether a
// failure is worth retrying: true for a transport error or a 5xx response,
// false for everything else (success, or a 4xx/other rejection).
func (s Sender) post(
	ctx context.Context, client *http.Client, n Notification, priority, tags string,
) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, strings.NewReader(n.Body))
	if err != nil {
		return false, fmt.Errorf("notify: building request: %w", err)
	}
	req.Header.Set("Title", n.Title)
	req.Header.Set("Priority", priority)
	req.Header.Set("Tags", tags)
	if s.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return true, fmt.Errorf("notify: sending request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode < http.StatusMultipleChoices:
		return false, nil
	case resp.StatusCode >= http.StatusInternalServerError:
		return true, fmt.Errorf("notify: server error: %s", resp.Status)
	default:
		return false, fmt.Errorf("notify: request rejected: %s", resp.Status)
	}
}

// ResolveSenderEnv resolves a Sender from environ (os.Environ() form,
// "KEY=VALUE" entries): CLAUDE_HOOKS_NTFY_URL or CLAUDE_HOOKS_NTFY_URL_FILE
// (file contents, whitespace-trimmed) supplies the URL, the same pattern
// supplies CLAUDE_HOOKS_NTFY_TOKEN, and CLAUDE_HOOKS_NTFY_DISABLED=true
// always forces not-ok. A pure function of the slice plus filesystem reads
// for the _FILE variants — never os.Getenv — so callers control exactly
// which environment it sees. No URL resolved (directly or via file) means
// not-ok.
func ResolveSenderEnv(environ []string) (Sender, bool) {
	env := parseEnviron(environ)
	if env["CLAUDE_HOOKS_NTFY_DISABLED"] == "true" {
		return Sender{}, false
	}

	url := resolveEnvValue(env, "CLAUDE_HOOKS_NTFY_URL")
	if url == "" {
		return Sender{}, false
	}
	token := resolveEnvValue(env, "CLAUDE_HOOKS_NTFY_TOKEN")

	return Sender{URL: url, Token: token}, true
}

// parseEnviron turns an os.Environ()-form slice into a lookup map, skipping
// any malformed entry with no "=".
func parseEnviron(environ []string) map[string]string {
	env := make(map[string]string, len(environ))
	for _, kv := range environ {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		env[key] = value
	}
	return env
}

// resolveEnvValue prefers env[base] directly; failing that, it reads
// env[base+"_FILE"] and trims whitespace. Any failure to resolve — key
// unset, file path unset, file missing or unreadable — returns "" so the
// caller falls through to its own not-configured handling.
func resolveEnvValue(env map[string]string, base string) string {
	if v := env[base]; v != "" {
		return v
	}
	path := env[base+"_FILE"]
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path) //nolint:gosec // Path comes from trusted env config, not external input
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
