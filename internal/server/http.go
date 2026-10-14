package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aleslanger/yahoo-mail-mcp/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/time/rate"
)

const (
	maxHTTPRequestBytes    = 16 << 20
	httpSessionTimeout     = 30 * time.Minute
	httpReadHeaderTimeout  = 10 * time.Second
	httpShutdownTimeout    = 5 * time.Second
	maxFailedHTTPAuthBurst = 10
	failedHTTPAuthInterval = 6 * time.Second
)

// NewHTTPHandler serves MCP over Streamable HTTP for a single local user.
// Layers, outermost first: cross-origin rejection, bearer token check with
// throttling of failed attempts, then the SDK handler, which also rejects
// DNS-rebinding requests whose Host header is not localhost.
func NewHTTPHandler(cfg config.Config, log *slog.Logger, token string) http.Handler {
	srv := NewWithLogger(cfg, log)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{
		SessionTimeout:      httpSessionTimeout,
		MaxRequestBodyBytes: maxHTTPRequestBytes,
		Logger:              log,
	})
	failures := rate.NewLimiter(rate.Every(failedHTTPAuthInterval), maxFailedHTTPAuthBurst)
	return http.NewCrossOriginProtection().Handler(requireBearerToken(mcpHandler, token, failures, log))
}

func requireBearerToken(next http.Handler, token string, failures *rate.Limiter, log *slog.Logger) http.Handler {
	expected := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fields := strings.Fields(r.Header.Get("Authorization"))
		if len(fields) == 2 && strings.EqualFold(fields[0], "bearer") {
			presented := sha256.Sum256([]byte(fields[1]))
			if subtle.ConstantTimeCompare(presented[:], expected[:]) == 1 {
				next.ServeHTTP(w, r)
				return
			}
		}
		if !failures.Allow() {
			log.Warn("HTTP authentication throttled", "remote", r.RemoteAddr)
			http.Error(w, "too many failed authentication attempts", http.StatusTooManyRequests)
			return
		}
		log.Warn("HTTP authentication failed", "remote", r.RemoteAddr)
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "missing or invalid bearer token", http.StatusUnauthorized)
	})
}

// RunHTTP listens on the configured loopback address until ctx is canceled.
func RunHTTP(ctx context.Context, cfg config.Config, log *slog.Logger, token string) error {
	if token == "" {
		return errors.New("HTTP token is required")
	}
	httpServer := &http.Server{
		Addr:              cfg.HTTPListen,
		Handler:           NewHTTPHandler(cfg, log, token),
		ReadHeaderTimeout: httpReadHeaderTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.ListenAndServe() }()
	log.Info("HTTP transport listening", "address", cfg.HTTPListen)
	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return nil
	}
}
