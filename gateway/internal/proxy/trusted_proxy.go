package proxy

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"time"

	"collab-docs-platform/gateway/internal/middleware"

	"github.com/labstack/echo/v4"
)

const (
	GatewayKeyHeader = "X-Gateway-Key"
	UserIDHeader     = "X-User-ID"
	UserRoleHeader   = "X-User-Role"
)

// NewTrustedProxy is the edge-auth boundary. The gateway has already run
// JWTMiddleware; this forwards the identity it established as X-User-ID and
// X-User-Role, with X-Gateway-Key as proof the request passed through here.
// All three headers are overwritten, never merged: whatever a client put in
// them is discarded. The Authorization header stops here - no backend ever
// sees the credential, so no backend can leak it or be tempted to validate
// it with a second copy of jwt.go.
//
// httputil.ReverseProxy transparently proxies WebSocket upgrades as well
// (month 2): it detects Connection: Upgrade, hijacks the TCP connection and
// pipes bytes both ways.
func NewTrustedProxy(targetURL string, gatewayKey string) (echo.HandlerFunc, error) {
	target, err := url.Parse(targetURL)
	if err != nil {
		return nil, err
	}
	// url.Parse accepts "doc-service:9001" (scheme "doc-service", no host)
	// without complaint; the proxy would start, report healthy, and 502 every
	// request. Fail at startup instead, like a missing secret does.
	if (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return nil, fmt.Errorf("proxy target %q must be an http(s) URL with a host", targetURL)
	}

	rp := httputil.NewSingleHostReverseProxy(target)
	// A backend that stops answering must not hold gateway goroutines
	// forever. Headers only: the body timeout is left open for WebSocket
	// streams (month 2).
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	rp.Transport = transport
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.Error("proxy error", "service", "gateway", "target", targetURL, "path", r.URL.Path, "error", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "backend unavailable"})
	}

	return func(c echo.Context) error {
		requestID, _ := c.Get(middleware.RequestIDKey).(string)
		headers := c.Request().Header

		if requestID != "" {
			headers.Set(middleware.RequestIDHeader, requestID)
		}

		headers.Set(GatewayKeyHeader, gatewayKey)

		headers.Del(UserIDHeader)
		headers.Del(UserRoleHeader)
		if userID, ok := c.Get(middleware.UserIDKey).(int64); ok {
			headers.Set(UserIDHeader, strconv.FormatInt(userID, 10))
			if role, ok := c.Get(middleware.RoleKey).(string); ok && role != "" {
				headers.Set(UserRoleHeader, role)
			}
		}

		headers.Del("Authorization")

		slog.Info("proxying request",
			"service", "gateway",
			"target", targetURL,
			"path", c.Request().URL.Path,
			"request_id", requestID,
		)

		rp.ServeHTTP(c.Response(), c.Request())
		return nil
	}, nil
}
