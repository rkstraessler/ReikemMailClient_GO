package reikemmail

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Config contains the Mail API URL, credentials and HTTP time limits.
type Config struct {
	BaseURL        string        // Origin, proxy prefix, or existing send URL.
	AuthKey        string        // Full credential sent as an Authorization bearer token.
	AdapterKey     string        // Credential sent in X-Adapter-Key.
	ConnectTimeout time.Duration // TCP and TLS establishment. Zero defaults to 10 seconds.
	RequestTimeout time.Duration // Entire HTTP exchange. Zero defaults to 30 seconds.
}

// NewClient creates a client for the v2 API. Both credentials must contain only
// visible ASCII characters, excluding spaces. Use HTTPS for remote APIs.
func NewClient(config Config) (*Client, error) {
	if !validKey(config.AuthKey) {
		return nil, errors.New("invalid auth key")
	}
	if !validKey(config.AdapterKey) {
		return nil, errors.New("invalid adapter key")
	}
	const maxTimeout = 2147483 * time.Second
	if config.ConnectTimeout < 0 || config.RequestTimeout < 0 ||
		config.ConnectTimeout > maxTimeout || config.RequestTimeout > maxTimeout {
		return nil, errors.New("connect and request timeouts must be between zero and 2147483 seconds")
	}
	if config.ConnectTimeout == 0 {
		config.ConnectTimeout = 10 * time.Second
	}
	if config.RequestTimeout == 0 {
		config.RequestTimeout = 30 * time.Second
	}
	root, err := endpointRoot(config.BaseURL)
	if err != nil {
		return nil, err
	}

	dialer := &net.Dialer{Timeout: config.ConnectTimeout}
	// The TLS dialer applies the same deadline to dialing and the handshake.
	tlsDialer := &tls.Dialer{NetDialer: dialer}
	return &Client{
		endpointRoot:   root,
		authKey:        config.AuthKey,
		adapterKey:     config.AdapterKey,
		requestTimeout: config.RequestTimeout,
		transport: &http.Transport{
			DialContext:        dialer.DialContext,
			DialTLSContext:     tlsDialer.DialContext,
			DisableKeepAlives:  true,
			DisableCompression: true,
		},
	}, nil
}

func validKey(key string) bool {
	return key != "" && strings.IndexFunc(key, func(r rune) bool { return r < '!' || r > '~' }) < 0
}

func endpointRoot(baseURL string) (string, error) {
	invalid := errors.New("invalid Mail API URL")
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", invalid
	}
	if port := parsed.Port(); port != "" {
		if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			return "", invalid
		}
	}
	prefix := strings.TrimRight(normalizeURLPath(parsed.EscapedPath()), "/")
	if strings.HasSuffix(prefix, "/api/v2/send") {
		prefix = strings.TrimSuffix(prefix, "/api/v2/send")
	} else {
		prefix = strings.TrimSuffix(prefix, "/api/send")
	}
	prefix = strings.TrimSuffix(prefix, "/api/v2")
	origin := url.URL{Scheme: parsed.Scheme, Host: parsed.Host}
	return origin.String() + prefix + "/api/v2", nil
}

func normalizeURLPath(path string) string {
	// Resolve URL dot segments while preserving repeated slashes and encoded slashes.
	parts := strings.Split(path, "/")
	kept := parts[:0]
	for _, part := range parts {
		switch strings.ReplaceAll(strings.ToLower(part), "%2e", ".") {
		case ".":
		case "..":
			if len(kept) > 1 {
				kept = kept[:len(kept)-1]
			}
		default:
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, "/")
}
