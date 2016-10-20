package defaults

type Upstream struct {
	Secure               bool
	MaxFails             int
	FailTimeout          int
	ConnectTimeout       int
	SendTimeout          int
	ReadTimeout          int
	BufferSize           string
	WhitelistSourceRange []string
}

type Backend struct {
	// enables which HTTP codes should be passed for processing with the error_page directive
	// http://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_intercept_errors
	// http://nginx.org/en/docs/http/ngx_http_core_module.html#error_page
	// By default this is disabled
	CustomHTTPErrors []int `structs:"custom-http-errors,-"`

	// Timeout in seconds for reading a response from the proxied server. The timeout is set only between
	// two successive read operations, not for the transmission of the whole response
	// http://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_read_timeout
	ProxyReadTimeout int `structs:"proxy-read-timeout,omitempty"`

	// Timeout in seconds for transmitting a request to the proxied server. The timeout is set only between
	// two successive write operations, not for the transmission of the whole request.
	// http://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_send_timeout
	ProxySendTimeout int `structs:"proxy-send-timeout,omitempty"`

	// Sets the size of the buffer used for reading the first part of the response received from the
	// proxied server. This part usually contains a small response header.
	// http://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_buffer_size)
	ProxyBufferSize string `structs:"proxy-buffer-size,omitempty"`

	// Configures name servers used to resolve names of upstream servers into addresses
	// http://nginx.org/en/docs/http/ngx_http_core_module.html#resolver
	Resolver string `structs:"resolver,omitempty"`

	// SkipAccessLogURLs sets a list of URLs that should not appear in the NGINX access log
	// This is useful with urls like `/health` or `health-check` that make "complex" reading the logs
	// By default this list is empty
	SkipAccessLogURLs []string `structs:"skip-access-log-urls,-"`

	// Enables or disables the redirect (301) to the HTTPS port
	SSLRedirect bool `structs:"ssl-redirect,omitempty"`

	// Number of unsuccessful attempts to communicate with the server that should happen in the
	// duration set by the fail_timeout parameter to consider the server unavailable
	// http://nginx.org/en/docs/http/ngx_http_upstream_module.html#upstream
	// Default: 0, ie use platform liveness probe
	UpstreamMaxFails int `structs:"upstream-max-fails,omitempty"`

	// Time during which the specified number of unsuccessful attempts to communicate with
	// the server should happen to consider the server unavailable
	// http://nginx.org/en/docs/http/ngx_http_upstream_module.html#upstream
	// Default: 0, ie use platform liveness probe
	UpstreamFailTimeout int `structs:"upstream-fail-timeout,omitempty"`

	// WhitelistSourceRange allows limiting access to certain client addresses
	// http://nginx.org/en/docs/http/ngx_http_access_module.html
	WhitelistSourceRange []string `structs:"whitelist-source-range,-"`
}
