package catalog

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// This file is the catalog's egress credential: a bearer token presented to a
// named catalog host so a private index and its pack files fetch like a public
// one. A token belongs to exactly one host and never leaves it: not to another
// host, not over plaintext, and not across a redirect that changes either.

// HostTokens maps a catalog host to the bearer token presented when fetching
// from it. A host absent from the map is fetched unauthenticated.
type HostTokens map[string]string

// ParseHostTokens parses "host=token" entries into a HostTokens map. A blank
// entry is skipped; a malformed one, a repeated host, or an entry carrying a
// scheme or path is an error rather than a silently unauthenticated fetch.
func ParseHostTokens(entries []string) (HostTokens, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	out := HostTokens{}
	for _, raw := range entries {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		host, token, ok := strings.Cut(entry, "=")
		host = strings.ToLower(strings.TrimSpace(host))
		token = strings.TrimSpace(token)
		// The token itself never reaches an error message: an operator reading a
		// startup failure out of a log should not find their credential in it.
		if !ok || host == "" || token == "" {
			return nil, fmt.Errorf("catalog: token entry %d is not host=token", len(out)+1)
		}
		if strings.ContainsAny(host, "/:") {
			return nil, fmt.Errorf("catalog: token host %q carries a scheme, port or path; name the host alone", host)
		}
		if _, dup := out[host]; dup {
			return nil, fmt.Errorf("catalog: token host %q is configured twice", host)
		}
		out[host] = token
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// Hosts lists the hosts a token is held for, in no particular order. It exists
// so a caller can log or display which hosts are authenticated without ever
// touching a token value.
func (t HostTokens) Hosts() []string {
	out := make([]string, 0, len(t))
	for host := range t {
		out = append(out, host)
	}
	return out
}

// tokenFor returns the token to present for rawURL, empty when none applies.
// A token is presented only over https and only to the exact host it was
// configured for: a suffix match would hand a credential to any host that
// merely ends in the configured name.
func (t HostTokens) tokenFor(u *url.URL) string {
	if len(t) == 0 || u == nil || u.Scheme != "https" {
		return ""
	}
	return t[strings.ToLower(u.Hostname())]
}

// has reports whether a token is held for u's host at all, whatever the scheme.
// tokenFor answers what is presented; this answers what is configured, and the
// difference is what keeps a plaintext URL with a token behind it from being
// told it has none.
func (t HostTokens) has(u *url.URL) bool {
	if len(t) == 0 || u == nil {
		return false
	}
	_, ok := t[strings.ToLower(u.Hostname())]
	return ok
}

// Fetch is the token-aware Fetcher: HTTPFetch's request, plus this host's
// bearer token when one is configured. It is what production wires into a
// Remote; HTTPFetch is the same call with no tokens held.
func (t HostTokens) Fetch(ctx context.Context, rawURL string) ([]byte, error) {
	return fetchURL(ctx, rawURL, t)
}

// authIsMissing reports whether status is one a private catalog gives an
// unauthenticated reader and no token was held for the host, which is the
// difference between a catalog that is gone and one that is not yours.
func authIsMissing(status int, tokens HostTokens, u *url.URL) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
	default:
		return false
	}
	return u != nil && !tokens.has(u)
}

// authorizedClient returns the client used when a token is attached: the
// default transport, plus a redirect rule that strips the credential the
// moment a hop leaves the token's host or drops out of https. Go already
// declines to copy Authorization across a domain change, but a redirect from
// a host to its own subdomain is not a domain change to the standard library
// and is one here.
func authorizedClient(host string) *http.Client {
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			if req.URL.Scheme != "https" || !strings.EqualFold(req.URL.Hostname(), host) {
				req.Header.Del("Authorization")
			}
			return nil
		},
	}
}
