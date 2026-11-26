package workspace

// RuntimeBuiltinModulesV0130 is the frozen 0.13.0 set of bare specifiers that
// name runtime modules rather than packages: Node 22 core module names
// (node:module builtinModules), plus "bun". Scheme-prefixed forms such as
// node:fs and bun:sqlite are already not packages. Bun's own builtinModules
// additionally lists ws and undici, which are npm packages Bun polyfills; they
// are deliberately absent, so an undeclared import of either is still a closure
// gap. The set is part of the contract, so it does not follow the Bun on PATH.
func RuntimeBuiltinModulesV0130() map[string]bool {
	builtins := make(map[string]bool, len(runtimeBuiltinModulesV0130))
	for _, name := range runtimeBuiltinModulesV0130 {
		builtins[name] = true
	}
	return builtins
}

var runtimeBuiltinModulesV0130 = []string{
	"_http_agent", "_http_client", "_http_common", "_http_incoming", "_http_outgoing", "_http_server",
	"_stream_duplex", "_stream_passthrough", "_stream_readable", "_stream_transform", "_stream_wrap", "_stream_writable",
	"_tls_common", "_tls_wrap", "assert", "assert/strict", "async_hooks", "buffer", "bun", "child_process", "cluster",
	"console", "constants", "crypto", "dgram", "diagnostics_channel", "dns", "dns/promises", "domain", "events", "fs",
	"fs/promises", "http", "http2", "https", "inspector", "inspector/promises", "module", "net", "os", "path",
	"path/posix", "path/win32", "perf_hooks", "process", "punycode", "querystring", "readline", "readline/promises",
	"repl", "stream", "stream/consumers", "stream/promises", "stream/web", "string_decoder", "sys", "timers",
	"timers/promises", "tls", "trace_events", "tty", "url", "util", "util/types", "v8", "vm", "wasi", "worker_threads", "zlib",
}
