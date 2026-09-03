package xai

import (
	"net/http"
	"os"
	"strings"
)

const (
	CLIProxyHost        = "cli-chat-proxy.grok.com"
	CLIClientVersion    = "0.2.93"
	CLIClientIdentifier = "grok-shell"
	CLITokenAuth        = "xai-grok-cli"
	CLIVersionEnv       = "XAI_GROK_CLI_VERSION"
)

// ResolveCLIVersion returns the configured Grok CLI identity version.
func ResolveCLIVersion() string {
	if version := strings.TrimSpace(os.Getenv(CLIVersionEnv)); version != "" {
		return version
	}
	return CLIClientVersion
}

// CLIUserAgent builds the User-Agent expected by the Grok CLI proxy.
func CLIUserAgent(version string) string {
	if strings.TrimSpace(version) == "" {
		version = ResolveCLIVersion()
	}
	return "xai-grok-workspace/" + version
}

// ApplyCLIProxyHeaders identifies requests sent to the OAuth CLI proxy. API
// requests to api.x.ai are deliberately left unchanged.
func ApplyCLIProxyHeaders(req *http.Request) {
	if req == nil || req.URL == nil || !strings.EqualFold(req.URL.Hostname(), CLIProxyHost) {
		return
	}
	version := ResolveCLIVersion()
	req.Header.Set("X-XAI-Token-Auth", CLITokenAuth)
	req.Header.Set("X-Grok-Client-Version", version)
	req.Header.Set("x-grok-client-version", version)
	req.Header.Set("x-grok-client-identifier", CLIClientIdentifier)
	req.Header.Set("X-Grok-Client-Mode", "interactive")
	req.Header.Set("User-Agent", CLIUserAgent(version))
}
