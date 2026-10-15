package replaceresponse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"go.uber.org/zap"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func TestBufferedContentLength(t *testing.T) {
	const (
		source   = "hello __NAME__"
		replaced = "hello world"
	)

	for _, tc := range []struct {
		name       string
		method     string
		status     int
		wantLength string // empty means the header must be absent
		wantBody   string
	}{
		{name: "GET reports replaced length", method: http.MethodGet, status: http.StatusOK, wantLength: strconv.Itoa(len(replaced)), wantBody: replaced},
		{name: "HEAD drops stale length", method: http.MethodHead, status: http.StatusOK},
		{name: "204 drops stale length", method: http.MethodGet, status: http.StatusNoContent},
		{name: "304 drops stale length", method: http.MethodGet, status: http.StatusNotModified},
		{name: "CONNECT 200 drops stale length", method: http.MethodConnect, status: http.StatusOK},
		{name: "CONNECT 407 reports replaced length", method: http.MethodConnect, status: http.StatusProxyAuthRequired, wantLength: strconv.Itoa(len(replaced)), wantBody: replaced},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// next behaves like file_server: it advertises the length of the
			// original body and writes a body only when one is expected
			next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Content-Length", strconv.Itoa(len(source)))
				w.Header().Set("Etag", `"abc"`)
				w.WriteHeader(tc.status)
				if tc.wantBody != "" {
					_, err := w.Write([]byte(source))
					return err
				}
				return nil
			})

			h := &Handler{
				Replacements: []*Replacement{{Search: "__NAME__", Replace: "world"}},
				logger:       zap.NewNop(),
			}
			req := httptest.NewRequest(tc.method, "/", nil)
			req = req.WithContext(context.WithValue(req.Context(), caddy.ReplacerCtxKey, caddy.NewReplacer()))
			rec := httptest.NewRecorder()
			if err := h.ServeHTTP(rec, req, next); err != nil {
				t.Fatalf("ServeHTTP: %v", err)
			}

			if rec.Code != tc.status {
				t.Errorf("expected status %d, got %d", tc.status, rec.Code)
			}
			gotLength, ok := rec.Header()["Content-Length"]
			switch {
			case tc.wantLength == "" && ok:
				t.Errorf("expected no Content-Length, got %q", gotLength)
			case tc.wantLength != "" && rec.Header().Get("Content-Length") != tc.wantLength:
				t.Errorf("expected Content-Length %q, got %q", tc.wantLength, rec.Header().Get("Content-Length"))
			}
			if body := rec.Body.String(); body != tc.wantBody {
				t.Errorf("expected body %q, got %q", tc.wantBody, body)
			}
			if got := rec.Header().Get("Etag"); got != `"abc"` {
				t.Errorf("expected Etag to be preserved, got %q", got)
			}
		})
	}
}
