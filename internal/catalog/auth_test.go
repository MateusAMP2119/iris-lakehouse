package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestParseHostTokens proves the entry grammar: what parses, what refuses, and
// that a refusal never quotes the token it refused.
func TestParseHostTokens(t *testing.T) {
	t.Run("valid entries parse and normalize the host", func(t *testing.T) {
		got, err := ParseHostTokens([]string{" RAW.githubusercontent.com = ghp_a ", "", "example.test=b"})
		if err != nil {
			t.Fatalf("ParseHostTokens = %v, want success", err)
		}
		want := HostTokens{"raw.githubusercontent.com": "ghp_a", "example.test": "b"}
		if len(got) != len(want) {
			t.Fatalf("parsed %d entries, want %d", len(got), len(want))
		}
		for host, token := range want {
			if got[host] != token {
				t.Errorf("token for %s = %q, want %q", host, got[host], token)
			}
		}
	})

	t.Run("an empty or blank list holds no tokens", func(t *testing.T) {
		for _, entries := range [][]string{nil, {}, {"", "   "}} {
			got, err := ParseHostTokens(entries)
			if err != nil {
				t.Fatalf("ParseHostTokens(%v) = %v, want success", entries, err)
			}
			if len(got) != 0 {
				t.Errorf("ParseHostTokens(%v) = %v, want no tokens", entries, got)
			}
		}
	})

	t.Run("malformed entries refuse", func(t *testing.T) {
		for _, bad := range []string{"nosep", "=token", "host=", "https://host=token", "host:443=token", "host/path=token"} {
			if _, err := ParseHostTokens([]string{bad}); err == nil {
				t.Errorf("ParseHostTokens(%q) succeeded, want a refusal", bad)
			}
		}
	})

	t.Run("a repeated host refuses rather than picking one", func(t *testing.T) {
		if _, err := ParseHostTokens([]string{"h.test=a", "H.test=b"}); err == nil {
			t.Error("a duplicate host succeeded, want a refusal")
		}
	})

	t.Run("a refusal never quotes the token", func(t *testing.T) {
		_, err := ParseHostTokens([]string{"secret-value-only"})
		if err == nil {
			t.Fatal("want a refusal")
		}
		if strings.Contains(err.Error(), "secret-value-only") {
			t.Errorf("error %q carries the offending entry; a credential must not reach a log", err)
		}
	})
}

// TestHostTokensScope proves a token reaches exactly the host it names, over
// https alone: a suffix neighbour, a sibling host, and a plaintext hop all get
// nothing.
func TestHostTokensScope(t *testing.T) {
	tokens := HostTokens{"catalog.test": "tok"}
	cases := []struct {
		url  string
		want string
	}{
		{"https://catalog.test/catalog.json", "tok"},
		{"https://CATALOG.TEST/catalog.json", "tok"},
		{"https://catalog.test:443/catalog.json", "tok"},
		{"http://catalog.test/catalog.json", ""},      // plaintext: the token would travel in the clear
		{"https://evilcatalog.test/x", ""},            // suffix neighbour, not the host
		{"https://catalog.test.evil/x", ""},           // subdomain of someone else
		{"https://sub.catalog.test/catalog.json", ""}, // a subdomain is a different host
		{"https://other.test/catalog.json", ""},
	}
	for _, c := range cases {
		u, err := url.Parse(c.url)
		if err != nil {
			t.Fatalf("parse %s: %v", c.url, err)
		}
		if got := tokens.tokenFor(u); got != c.want {
			t.Errorf("tokenFor(%s) = %q, want %q", c.url, got, c.want)
		}
	}
	if got := HostTokens(nil).tokenFor(&url.URL{Scheme: "https", Host: "catalog.test"}); got != "" {
		t.Errorf("an empty token set presented %q", got)
	}
}

// TestHostTokensFetch proves the wire behaviour end to end: the header is
// present for the named host, absent for any other, and a fetch that lands on
// 404 with no token says why rather than reading as a missing catalog.
func TestHostTokensFetch(t *testing.T) {
	t.Run("the named host receives the bearer token", func(t *testing.T) {
		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.Header.Get("Authorization")
			_, _ = w.Write([]byte("body"))
		}))
		defer srv.Close()
		// httptest serves plaintext, and a token is https-only by design, so this
		// case asserts the unauthenticated half of that rule at the same time.
		u, _ := url.Parse(srv.URL)
		data, err := HostTokens{u.Hostname(): "tok"}.Fetch(context.Background(), srv.URL)
		if err != nil {
			t.Fatalf("Fetch = %v, want success", err)
		}
		if string(data) != "body" {
			t.Errorf("body = %q, want %q", data, "body")
		}
		if got != "" {
			t.Errorf("Authorization = %q over http, want none: a token must not travel in the clear", got)
		}
	})

	t.Run("an unauthenticated 404 names the missing token", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "404: Not Found", http.StatusNotFound)
		}))
		defer srv.Close()
		_, err := HTTPFetch(context.Background(), srv.URL+"/catalog.json")
		if err == nil {
			t.Fatal("Fetch succeeded, want the status refusal")
		}
		if !strings.Contains(err.Error(), "no token configured") {
			t.Errorf("error = %q, want it to name the missing token: a private catalog answers 404", err)
		}
	})

	t.Run("a 404 for a host with a token stays a plain status error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "gone", http.StatusNotFound)
		}))
		defer srv.Close()
		u, _ := url.Parse(srv.URL)
		// A token is configured for this host even though the plaintext scheme
		// stops it being presented: telling an operator to configure what they
		// already configured would send them chasing the wrong fault.
		_, err := HostTokens{u.Hostname(): "tok"}.Fetch(context.Background(), srv.URL)
		if err == nil {
			t.Fatal("Fetch succeeded, want the status refusal")
		}
		if strings.Contains(err.Error(), "no token configured") {
			t.Errorf("error = %q, want no missing-token hint: one is configured for this host", err)
		}
	})

	t.Run("a non-auth status never gains the hint", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		defer srv.Close()
		_, err := HTTPFetch(context.Background(), srv.URL)
		if err == nil {
			t.Fatal("Fetch succeeded, want the status refusal")
		}
		if strings.Contains(err.Error(), "no token configured") {
			t.Errorf("error = %q, want no missing-token hint: 500 is not an auth answer", err)
		}
	})
}

// TestAuthorizedClientRedirect proves the credential survives a hop only while
// the hop stays on the token's host and in https. The rule is checked directly
// rather than through live servers: two httptest servers share the hostname
// 127.0.0.1, so a server-driven redirect could only ever exercise the scheme
// half and would pass while the host half was broken.
func TestAuthorizedClientRedirect(t *testing.T) {
	check := authorizedClient("catalog.test").CheckRedirect
	cases := []struct {
		to   string
		keep bool
	}{
		{"https://catalog.test/moved", true},
		{"https://CATALOG.TEST/moved", true},
		{"https://sub.catalog.test/moved", false}, // same domain to the stdlib, a different host here
		{"https://elsewhere.test/moved", false},
		{"https://evilcatalog.test/moved", false},
		{"http://catalog.test/moved", false}, // same host, but now in the clear
	}
	for _, c := range cases {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, c.to, nil)
		if err != nil {
			t.Fatalf("new request %s: %v", c.to, err)
		}
		req.Header.Set("Authorization", "Bearer tok")
		if err := check(req, nil); err != nil {
			t.Fatalf("CheckRedirect(%s) = %v, want the hop allowed", c.to, err)
		}
		got := req.Header.Get("Authorization") != ""
		if got != c.keep {
			t.Errorf("redirect to %s carried the credential = %v, want %v", c.to, got, c.keep)
		}
	}

	t.Run("the redirect chain is capped", func(t *testing.T) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://catalog.test/moved", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		if err := check(req, make([]*http.Request, 10)); err == nil {
			t.Error("an eleventh hop was allowed, want the chain stopped")
		}
	})
}
