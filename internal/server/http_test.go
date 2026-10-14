package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aleslanger/yahoo-mail-mcp/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testHTTPToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

func httpFixture(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := config.Config{Accounts: []config.Account{{Alias: "personal", Address: "personal@example.com"}}}
	srv := httptest.NewServer(NewHTTPHandler(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), testHTTPToken))
	t.Cleanup(srv.Close)
	return srv
}

func postInitialize(t *testing.T, srv *httptest.Server, mutate func(*http.Request)) int {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	mutate(req)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestHTTPRejectsMissingAndWrongToken(t *testing.T) {
	srv := httpFixture(t)
	if code := postInitialize(t, srv, func(*http.Request) {}); code != http.StatusUnauthorized {
		t.Fatalf("missing token: status %d", code)
	}
	if code := postInitialize(t, srv, func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong") }); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: status %d", code)
	}
}

func TestHTTPServesToolsWithValidToken(t *testing.T) {
	srv := httpFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: &http.Client{Transport: bearerTransport{token: testHTTPToken, base: http.DefaultTransport}}}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) == 0 {
		t.Fatalf("list tools: %v", err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "account_list", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("account_list over HTTP: %+v, %v", result, err)
	}
}

func TestHTTPRejectsCrossOriginAndRebindingWithValidToken(t *testing.T) {
	srv := httpFixture(t)
	authorize := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+testHTTPToken) }
	if code := postInitialize(t, srv, func(r *http.Request) { authorize(r); r.Header.Set("Sec-Fetch-Site", "cross-site") }); code != http.StatusForbidden {
		t.Fatalf("cross-origin request: status %d", code)
	}
	if code := postInitialize(t, srv, func(r *http.Request) { authorize(r); r.Host = "attacker.example" }); code != http.StatusForbidden {
		t.Fatalf("DNS rebinding Host header: status %d", code)
	}
	if code := postInitialize(t, srv, authorize); code != http.StatusOK {
		t.Fatalf("same-origin request: status %d", code)
	}
}

func TestHTTPThrottlesRepeatedAuthenticationFailures(t *testing.T) {
	srv := httpFixture(t)
	wrong := func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong") }
	throttled := false
	for range maxFailedHTTPAuthBurst + 1 {
		if postInitialize(t, srv, wrong) == http.StatusTooManyRequests {
			throttled = true
		}
	}
	if !throttled {
		t.Fatal("repeated authentication failures were not throttled")
	}
	if code := postInitialize(t, srv, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+testHTTPToken) }); code != http.StatusOK {
		t.Fatalf("valid token blocked by failure throttling: status %d", code)
	}
}
