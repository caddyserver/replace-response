Caddy `replace_response` handler module
=====================================

This Caddy module performs substring and regular expression replacements on response bodies, hence the name `replace_response`.

By default, this module operates in "buffer" mode. This is not very memory-efficient, but it guarantees we can always set the correct Content-Length header because we can buffer the output to know the resulting length before writing the response. If you need higher efficiency, you can enable "streaming" mode. When performing replacements on a stream, the Content-Length header may be removed because it is not always possible to know the correct value, since the results are streamed directly to the client and headers must be written before the body.

**Module name:** `http.handlers.replace_response`


## JSON examples

Substring substitution:

```json
{
	"handler": "replace_response",
	"replacements": [
		{
			"search": "Foo",
			"replace": "Bar"
		}
	]
}
```

Regular expression replacement:

```json
{
	"handler": "replace_response",
	"replacements": [
		{
			"search_regexp": "\\s+foo(bar|baz)\\s+",
			"replace": " foo $1 "
		}
	]
}
```

Same, but with streaming mode (we just set `"stream": true` in the handler):

```json
{
	"handler": "replace_response",
	"replacements": [
		{
			"search_regexp": "\\s+foo(bar|baz)\\s+",
			"replace": " foo $1 "
		}
	],
	"stream": true
}
```


## Caddyfile

This module has Caddyfile support. It registers the `replace` directive. Make sure to [order]() the handler directive in the correct place in the middleware chain; usually this works well:

```
{
	order replace after encode
}
```

Syntax:

```
replace [stream | [re] <search> <replace>] {
	stream
	[re] <search> <replace>
}
```

- `re` indicates a regular expression instead of substring.
- `stream` enables streaming mode.

Simple substring substitution:

```
replace Foo Bar
```

Regex replacement:

```
replace re "\s+foo(bar|baz)\s+" " foo $1 "
```

Streaming mode:

```
replace stream {
	Foo Bar
}
```

Multiple replacements:

```
replace {
	Foo Bar
	re "\s+foo(bar|baz)\s+" " foo $1 "
	A B
}
```
