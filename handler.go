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
	"golang.org/x/text/transform"
)

func init() {
	caddy.RegisterModule(Handler{})
}

// TODO: response matching? Should it be per-replacement or per group of replacements? probably per-handler (a handler is already a group of replacements)...

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

	transformerPool *sync.Pool
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

	h.transformerPool = &sync.Pool{
		New: func() interface{} {
			transforms := make([]transform.Transformer, len(h.Replacements))
			for i, repl := range h.Replacements {
				if repl.re != nil {
					transforms[i] = replace.RegexpString(repl.re, repl.Replace)
				} else {
					transforms[i] = replace.String(repl.Search, repl.Replace)
				}
			}
			return transform.Chain(transforms...)
		},
	}

	return nil
}

// ServeHTTP implements caddyhttp.MiddlewareHandler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {

	tr := h.transformerPool.Get().(transform.Transformer)
	tr.Reset()
	defer h.transformerPool.Put(tr)

	if h.Stream {
		// don't buffer response body, perform streaming replacement
		fw := &replaceWriter{
			ResponseWriterWrapper: &caddyhttp.ResponseWriterWrapper{ResponseWriter: w},
			tw:                    transform.NewWriter(w, tr),
			handler:               h,
		}
		defer fw.tw.Close()
		return next.ServeHTTP(fw, r)
	}

	// get a buffer to hold the response body
	respBuf := bufPool.Get().(*bytes.Buffer)
	respBuf.Reset()
	defer bufPool.Put(respBuf)

	// set up the response recorder
	shouldBuf := func(_ int, _ http.Header) bool { return true }
	rec := caddyhttp.NewResponseRecorder(w, respBuf, shouldBuf)

	// collect the response from upstream
	err := next.ServeHTTP(rec, r)
	if err != nil {
		return err
	}
	if !rec.Buffered() {
		return nil // should never happen, but whatever
	}

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
	handler     *Handler
}

func (fw *replaceWriter) WriteHeader(status int) {
	if fw.wroteHeader {
		return
	}
	fw.wroteHeader = true

	// we don't know the length after replacements since
	// we're not buffering it all to find out
	fw.Header().Del("Content-Length")

	fw.ResponseWriterWrapper.WriteHeader(status)
}

func (fw *replaceWriter) Write(d []byte) (int, error) {
	if !fw.wroteHeader {
		fw.WriteHeader(http.StatusOK)
	}
	return fw.tw.Write(d)
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
