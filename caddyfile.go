// Copyright 2015 Matthew Holt
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

package replaceresponse

import (
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func init() {
	httpcaddyfile.RegisterHandlerDirective("replace", parseCaddyfile)
}

// parseCaddyfile unmarshals tokens from h into a new Handler.
func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	handler := new(Handler)
	err := handler.UnmarshalCaddyfile(h.Dispenser)
	return handler, err
}

// UnmarshalCaddyfile implements caddyfile.Unmarshaler. Syntax:
//
//     replace [stream | [re] <search> <replace>] {
//          stream
//          [re] <search> <replace>
//     }
//
// If 're' is specified, the search string will be treated as a regular expression.
// If 'stream' is specified, the replacement will happen without buffering the
// whole response body; this might remove the Content-Length header.
func (h *Handler) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	line := func() error {
		var repl Replacement

		switch d.Val() {
		case "stream":
			if h.Stream {
				return d.Err("streaming already enabled")
			}
			h.Stream = true
			if d.NextArg() {
				return d.ArgErr()
			}
			return nil

		case "re":
			if !d.AllArgs(&repl.SearchRegexp, &repl.Replace) {
				return d.ArgErr()
			}

		default:
			repl.Search = d.Val()
			if !d.NextArg() {
				return d.ArgErr()
			}
			repl.Replace = d.Val()
			if d.NextArg() {
				return d.ArgErr()
			}
		}

		h.Replacements = append(h.Replacements, &repl)
		return nil
	}

	for d.Next() {
		if d.NextArg() {
			if err := line(); err != nil {
				return err
			}
		}
		for nesting := d.Nesting(); d.NextBlock(nesting); {
			if err := line(); err != nil {
				return err
			}
		}
	}
	return nil
}
