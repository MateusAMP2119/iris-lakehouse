package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// latin1Manha is "Correio da Manhã" in ISO-8859-1: the accent is the single
// byte 0xE3, which is not valid UTF-8 on its own.
var latin1Manha = []byte("Correio da Manh\xe3")

// TestDecodeUTF8 pins the normalization rule: a DECLARED non-UTF-8 encoding is
// decoded, and everything else -- no declaration, an unknown label, UTF-8
// already -- hands back the origin's own bytes rather than a guess.
func TestDecodeUTF8(t *testing.T) {
	tests := []struct {
		name        string
		body        []byte
		contentType string
		wantText    string
		wantCharset string
	}{
		{
			name:        "charset in the content type",
			body:        latin1Manha,
			contentType: "text/xml; charset=ISO-8859-1",
			wantText:    "Correio da Manhã",
			wantCharset: "iso-8859-1",
		},
		{
			name:     "charset in the xml declaration",
			body:     append([]byte(`<?xml version="1.0" encoding="iso-8859-1"?><t>`), latin1Manha...),
			wantText: `<?xml version="1.0" encoding="iso-8859-1"?><t>Correio da Manhã`,
			// The declaration now describes bytes that no longer exist; that is
			// documented, and the label still reports what was decoded from.
			wantCharset: "iso-8859-1",
		},
		{
			name:        "charset in an html meta",
			body:        append([]byte(`<html><head><meta charset="windows-1252">`), latin1Manha...),
			wantText:    `<html><head><meta charset="windows-1252">Correio da Manhã`,
			wantCharset: "windows-1252",
		},
		{
			name:        "the content type wins over the body",
			body:        append([]byte(`<?xml version="1.0" encoding="utf-8"?>`), latin1Manha...),
			contentType: "text/xml; charset=ISO-8859-1",
			wantText:    `<?xml version="1.0" encoding="utf-8"?>Correio da Manhã`,
			wantCharset: "iso-8859-1",
		},
		{
			name:        "utf-8 declared is left alone",
			body:        []byte("Correio da Manhã"),
			contentType: "application/xml; charset=utf-8",
			wantText:    "Correio da Manhã",
		},
		{
			name:        "an alias of utf-8 is left alone",
			body:        []byte("Correio da Manhã"),
			contentType: "application/xml; charset=UTF8",
			wantText:    "Correio da Manhã",
		},
		{
			name:     "no declaration and valid utf-8 is left alone",
			body:     []byte("Correio da Manhã"),
			wantText: "Correio da Manhã",
		},
		{
			name: "no declaration and invalid utf-8 is still left alone",
			body: latin1Manha,
			// Guessing would rewrite bytes the origin never declared; the honest
			// answer is what arrived, even though it will not survive JSON.
			wantText: string(latin1Manha),
		},
		{
			name:        "windows-1252 curly quotes",
			body:        []byte("diz \x93sim\x94 e \x80100"),
			contentType: "text/html; charset=windows-1252",
			wantText:    "diz “sim” e €100",
			wantCharset: "windows-1252",
		},
		{
			name:        "iso-8859-15 euro sign",
			body:        []byte("custa \xa4100 na Manh\xe3"),
			contentType: "text/plain; charset=iso-8859-15",
			wantText:    "custa €100 na Manhã",
			wantCharset: "iso-8859-15",
		},
		{
			name:        "a charset outside the latin family is left alone",
			body:        []byte("\x82\xa0\x82\xa2"),
			contentType: "text/html; charset=shift_jis",
			wantText:    "\x82\xa0\x82\xa2",
		},
		{
			name:        "an ascii body is untouched even when latin-1 is declared",
			body:        []byte("plain ascii"),
			contentType: "text/xml; charset=ISO-8859-1",
			wantText:    "plain ascii",
		},
		{
			name:        "an unknown label is left alone",
			body:        latin1Manha,
			contentType: "text/xml; charset=nonesuch-8",
			wantText:    string(latin1Manha),
		},
		{
			name:        "an empty body is left alone",
			body:        []byte{},
			contentType: "text/xml; charset=ISO-8859-1",
			wantText:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, charset := decodeUTF8(tt.body, tt.contentType)
			if string(got) != tt.wantText {
				t.Errorf("text = %q, want %q", got, tt.wantText)
			}
			if charset != tt.wantCharset {
				t.Errorf("charset = %q, want %q", charset, tt.wantCharset)
			}
		})
	}
}

// TestSourceFetchTranscodesDeclaredCharset pins the whole path an ISO-8859-1
// origin takes: the pipeline reads UTF-8 through the frame, while the capture's
// digest and byte count still describe the bytes the origin served.
func TestSourceFetchTranscodesDeclaredCharset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/xml; charset=ISO-8859-1")
		_, _ = w.Write(latin1Manha)
	}))
	defer srv.Close()

	answer := newSourceWatcher(nil).fetch(t.Context(), "p", srv.URL)

	status, changed, body := decodeSourceLine(t, answer.line)
	if status != 200 || !changed {
		t.Fatalf("fetch = (%d, %v), want (200, true)", status, changed)
	}
	if body != "Correio da Manhã" {
		t.Errorf("frame body = %q, want the transcoded text", body)
	}

	var summary struct {
		Bytes   int    `json:"bytes"`
		SHA256  string `json:"sha256"`
		Charset string `json:"charset"`
	}
	if err := json.Unmarshal([]byte(answer.summary), &summary); err != nil {
		t.Fatalf("summary does not parse: %v (%q)", err, answer.summary)
	}
	raw := sha256.Sum256(latin1Manha)
	if summary.SHA256 != hex.EncodeToString(raw[:]) {
		t.Errorf("summary sha256 = %s, want the digest of the served bytes", summary.SHA256)
	}
	if summary.Bytes != len(latin1Manha) {
		t.Errorf("summary bytes = %d, want %d (the served length)", summary.Bytes, len(latin1Manha))
	}
	if summary.Charset != "iso-8859-1" {
		t.Errorf("summary charset = %q, want iso-8859-1", summary.Charset)
	}
}

// TestSourceFetchLeavesUTF8Alone pins that the common case carries no charset in
// its provenance: nothing was decoded, so nothing is claimed.
func TestSourceFetchLeavesUTF8Alone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		_, _ = w.Write([]byte("Público"))
	}))
	defer srv.Close()

	answer := newSourceWatcher(nil).fetch(t.Context(), "p", srv.URL)
	if _, _, body := decodeSourceLine(t, answer.line); body != "Público" {
		t.Errorf("frame body = %q, want Público", body)
	}
	if strings.Contains(answer.summary, `"charset"`) {
		t.Errorf("summary claims a charset for an untouched body: %s", answer.summary)
	}
}
