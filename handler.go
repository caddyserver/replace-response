// Copyright 2020 Matthew Holt
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package replaceresponse registers a Caddy HTTP handler module that
// performs replacements on response bodies.
package replaceresponse

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"sync"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/icholy/replace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/text/transform"
)

func init() {
	caddy.RegisterModule(Handler{})
}

// Handler manipulates response bodies by performing
// substring or regex replacements.
type Handler struct {
	// The list of replacements to make on the response body.
	Replacements []*Replacement `json:"replacements,omitempty"`

	// If true, perform replacements in a streaming fashion.
	// This is more memory-efficient but can remove the
	// Content-Length header since knowing the correct length
	// is impossible without buffering, and getting it wrong
	// can break HTTP/2 streams.
	Stream bool `json:"stream,omitempty"`

	// Only run replacements on responses that match against this ResponseMmatcher.
	Matcher *caddyhttp.ResponseMatcher `json:"match,omitempty"`

	logger *zap.Logger
}

// CaddyModule returns the Caddy module information.
func (Handler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.replace_response",
		New: func() caddy.Module { return new(Handler) },
	}
}

// Provision implements caddy.Provisioner.
func (h *Handler) Provision(ctx caddy.Context) error {
	h.logger = ctx.Logger()

	if len(h.Replacements) == 0 {
		return fmt.Errorf("no replacements configured")
	}

	// prepare each replacement
	for i, repl := range h.Replacements {
		if repl.Search == "" && repl.SearchRegexp == "" {
			return fmt.Errorf("replacement %d: no search or search_regexp configured", i)
		}
		if repl.Search != "" && repl.SearchRegexp != "" {
			return fmt.Errorf("replacement %d: cannot specify both search and search_regexp in same replacement", i)
		}
		if repl.SearchRegexp != "" {
			re, err := regexp.Compile(repl.SearchRegexp)
			if err != nil {
				return fmt.Errorf("replacement %d: %v", i, err)
			}
			repl.re = re
		}
	}

	return nil
}

// ServeHTTP implements caddyhttp.MiddlewareHandler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	if h.Stream {
		if c := h.logger.Check(zapcore.DebugLevel, "streaming body replacement"); c != nil {
			c.Write(
				zap.Any("replacements", h.Replacements),
				zap.Object("request", caddyhttp.LoggableHTTPRequest{Request: r}),
			)
		}

		tr := h.makeTransformer(r)

		// don't buffer response body, perform streaming replacement
		fw := &replaceWriter{
			ResponseWriterWrapper: &caddyhttp.ResponseWriterWrapper{ResponseWriter: w},
			tr:                    tr,
			handler:               h,
		}
		err := next.ServeHTTP(fw, r)
		if err != nil {
			return err
		}
		// only close if there is no error; see PR #21
		// as of May 2023, Close() only flushes remaining bytes, but
		// this ends up calling WriteHeader() even if we don't want that
		fw.Close()

		return nil
	}

	// get a buffer to hold the response body
	respBuf := bufPool.Get().(*bytes.Buffer)
	respBuf.Reset()
	defer bufPool.Put(respBuf)

	// set up the response recorder
	shouldBuf := func(status int, headers http.Header) bool {
		if h.Matcher != nil {
			return h.Matcher.Match(status, headers)
		} else {
			// Always replace if no matcher is specified
			return true
		}
	}
	rec := caddyhttp.NewResponseRecorder(w, respBuf, shouldBuf)

	// collect the response from upstream
	err := next.ServeHTTP(rec, r)
	if err != nil {
		return err
	}
	if !rec.Buffered() {
		// Skipped, no need to replace
		if c := h.logger.Check(zapcore.DebugLevel, "not buffering body; skipping replacement"); c != nil {
			c.Write(
				zap.Int("response_status", rec.Status()),
				zap.Object("request", caddyhttp.LoggableHTTPRequest{Request: r}),
			)
		}
		return nil
	}

	if c := h.logger.Check(zapcore.DebugLevel, "buffered body replacement"); c != nil {
		c.Write(
			zap.Any("replacements", h.Replacements),
			zap.Object("request", caddyhttp.LoggableHTTPRequest{Request: r}),
		)
	}

	tr := h.makeTransformer(r)

	// TODO: could potentially use transform.Append here with a pooled byte slice as buffer?
	result, _, err := transform.Bytes(tr, rec.Buffer().Bytes())
	if err != nil {
		return err
	}

	// make sure length is correct, otherwise bad things can happen
	if w.Header().Get("Content-Length") != "" {
		w.Header().Set("Content-Length", strconv.Itoa(len(result)))
	}

	if status := rec.Status(); status > 0 {
		w.WriteHeader(status)
	}
	w.Write(result)

	return nil
}

func (h *Handler) makeTransformer(req *http.Request) transform.Transformer {
	reqReplacer := req.Context().Value(caddy.ReplacerCtxKey).(*caddy.Replacer)

	transforms := make([]transform.Transformer, len(h.Replacements))
	for i, repl := range h.Replacements {
		if repl.re != nil {
			tr := replace.RegexpIndexFunc(repl.re, func(src []byte, index []int) []byte {
				template := reqReplacer.ReplaceKnown(repl.Replace, "")
				return repl.re.Expand(nil, []byte(template), src, index)
			})
			// See: https://github.com/icholy/replace/issues/5#issuecomment-949757616
			tr.MaxMatchSize = 2048
			transforms[i] = tr
		} else {
			transforms[i] = replace.String(
				reqReplacer.ReplaceKnown(repl.Search, ""),
				reqReplacer.ReplaceKnown(repl.Replace, ""),
			)
		}
	}
	return transform.Chain(transforms...)
}

// Replacement is either a substring or regular expression replacement
// to perform; precisely one must be specified, not both.
type Replacement struct {
	// A substring to search for. Mutually exclusive with search_regexp.
	Search string `json:"search,omitempty"`

	// A regular expression to search for. Mutually exclusive with search.
	SearchRegexp string `json:"search_regexp,omitempty"`

	// The replacement string/value. Required.
	Replace string `json:"replace"`

	re *regexp.Regexp
}

// replaceWriter is used for streaming response body replacement. It
// ensures the Content-Length header is removed and writes to tw,
// which should be a transform writer that performs replacements.
type replaceWriter struct {
	*caddyhttp.ResponseWriterWrapper
	wroteHeader bool
	tw          io.WriteCloser
	tr          transform.Transformer
	handler     *Handler
}

func (fw *replaceWriter) WriteHeader(status int) {
	if fw.wroteHeader {
		return
	}
	fw.wroteHeader = true

	if fw.handler.Matcher == nil || fw.handler.Matcher.Match(status, fw.ResponseWriterWrapper.Header()) {
		// we don't know the length after replacements since
		// we're not buffering it all to find out
		fw.Header().Del("Content-Length")
		fw.tw = transform.NewWriter(fw.ResponseWriterWrapper, fw.tr)
	}

	fw.ResponseWriterWrapper.WriteHeader(status)
}

func (fw *replaceWriter) Write(d []byte) (int, error) {
	if !fw.wroteHeader {
		fw.WriteHeader(http.StatusOK)
	}

	if fw.tw != nil {
		return fw.tw.Write(d)
	} else {
		return fw.ResponseWriterWrapper.Write(d)
	}
}

func (fw *replaceWriter) Close() error {
	if fw.tw != nil {
		// Close if we have a transform writer, the underlying one does not need to be closed.
		return fw.tw.Close()
	}
	return nil
}

var bufPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

// Interface guards
var (
	_ caddy.Provisioner           = (*Handler)(nil)
	_ caddyhttp.MiddlewareHandler = (*Handler)(nil)
	_ caddyfile.Unmarshaler       = (*Handler)(nil)

	_ http.ResponseWriter = (*replaceWriter)(nil)
)
