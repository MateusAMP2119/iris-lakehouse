package daemon

import (
	"bytes"
	"mime"
	"regexp"
	"strings"
	"unicode/utf8"
)

// This file is the declared source's text normalization. A fetched body reaches
// a pipeline as a JSON string, and JSON strings are UTF-8: encoding a body that
// is not UTF-8 collapses every offending byte to U+FFFD before any script can
// see it, so an origin serving ISO-8859-1 would deliver its accents already
// destroyed. The fix belongs here rather than in every script, because by the
// time the script holds the frame the bytes are gone.
//
// Only a DECLARED encoding is honoured -- the Content-Type charset, an XML
// declaration, or an HTML meta charset -- and only from the single-byte Latin
// family decoded below. A body with no declaration is passed through untouched
// even when it is not valid UTF-8: guessing an encoding silently rewrites an
// origin's bytes into something it never served, and this engine would rather
// hand over exactly what arrived. So is a body declaring anything outside that
// family; a full charset library is a dependency this module does not carry, and
// a wrong decode is worse than an honest passthrough.
//
// The digest and byte count recorded for provenance are always the origin's own
// bytes, never these. One caveat rides along: a transcoded XML body still
// carries its original encoding declaration, now describing bytes that no longer
// exist. That is harmless for a script parsing the frame's string -- the
// declaration is advisory once the text is decoded -- but a script that
// re-encodes that string to bytes and hands them to a strict XML parser must
// ignore the declaration.

// charsetDeclRe finds an encoding declared inside the body itself: an XML
// declaration's encoding pseudo-attribute or an HTML meta charset. Every
// encoding decoded here is ASCII-compatible, so matching raw bytes is safe.
var charsetDeclRe = regexp.MustCompile(`(?i)(?:<\?xml[^>]*\bencoding\s*=\s*["']([\w.:-]+)["']|<meta[^>]+charset\s*=\s*["']?([\w.:-]+))`)

// charsetSniffLimit bounds how far into a body a declaration is looked for; a
// declaration that has not appeared by then is not a declaration.
const charsetSniffLimit = 2048

// cp1252High maps windows-1252's 0x80-0x9F block, the one range where it departs
// from ISO-8859-1. A slot the encoding leaves undefined maps to its own value,
// which is what a browser does with it.
var cp1252High = [32]rune{
	0x20AC, 0x81, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021,
	0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x8D, 0x017D, 0x8F,
	0x90, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
	0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x9D, 0x017E, 0x0178,
}

// latin9Diff maps the eight positions where ISO-8859-15 departs from
// ISO-8859-1, the euro sign among them.
var latin9Diff = map[byte]rune{
	0xA4: 0x20AC, 0xA6: 0x0160, 0xA8: 0x0161, 0xB4: 0x017D,
	0xB8: 0x017E, 0xBC: 0x0152, 0xBD: 0x0153, 0xBE: 0x0178,
}

// decodeUTF8 returns body as UTF-8 text alongside the canonical charset name it
// decoded from, which is empty when nothing was done. A body is left untouched
// when it declares no encoding, when it declares UTF-8, or when it declares an
// encoding outside the Latin family below: in each of those cases the original
// bytes are the honest answer.
func decodeUTF8(body []byte, contentType string) ([]byte, string) {
	if len(body) == 0 {
		return body, ""
	}
	name := canonicalCharset(declaredCharset(body, contentType))
	if name == "" || name == "utf-8" {
		return body, ""
	}
	if utf8.Valid(body) && isASCII(body) {
		// Pure ASCII is already its own transcoding in every encoding here.
		return body, ""
	}
	return decodeLatin(body, name), name
}

// canonicalCharset folds a declared label to the one name this file decodes by,
// or empty for a label outside the family it handles.
func canonicalCharset(label string) string {
	switch strings.ToLower(strings.Trim(strings.TrimSpace(label), `"'`)) {
	case "utf-8", "utf8", "unicode-1-1-utf-8", "us-ascii", "ascii":
		return "utf-8"
	case "iso-8859-1", "iso8859-1", "iso_8859-1", "latin1", "l1", "cp819", "iso-ir-100":
		return "iso-8859-1"
	case "windows-1252", "cp1252", "x-cp1252", "ansi_x3.110-1983":
		return "windows-1252"
	case "iso-8859-15", "iso8859-15", "iso_8859-15", "latin9", "l9":
		return "iso-8859-15"
	default:
		return ""
	}
}

// decodeLatin widens one single-byte Latin encoding to UTF-8. Every byte maps to
// exactly one rune in all three, so the decode cannot fail and needs no error.
func decodeLatin(body []byte, name string) []byte {
	out := make([]byte, 0, len(body)+len(body)/4)
	var buf [utf8.UTFMax]byte
	for _, b := range body {
		r := rune(b)
		switch {
		case b < 0x80:
			out = append(out, b)
			continue
		case name == "windows-1252" && b < 0xA0:
			r = cp1252High[b-0x80]
		case name == "iso-8859-15":
			if alt, ok := latin9Diff[b]; ok {
				r = alt
			}
		}
		out = append(out, buf[:utf8.EncodeRune(buf[:], r)]...)
	}
	return out
}

// isASCII reports whether every byte is seven-bit, the case where no encoding in
// this family would change anything.
func isASCII(body []byte) bool {
	for _, b := range body {
		if b >= 0x80 {
			return false
		}
	}
	return true
}

// declaredCharset reads the encoding an origin declared, preferring the
// Content-Type header over the body's own declaration because the header is what
// an HTTP client is told to trust. An empty result means nothing was declared.
func declaredCharset(body []byte, contentType string) string {
	if _, params, err := mime.ParseMediaType(contentType); err == nil {
		if cs := strings.TrimSpace(params["charset"]); cs != "" {
			return cs
		}
	}
	head := body
	if len(head) > charsetSniffLimit {
		head = head[:charsetSniffLimit]
	}
	m := charsetDeclRe.FindSubmatch(head)
	if m == nil {
		return ""
	}
	for _, group := range m[1:] {
		if len(group) > 0 {
			return string(bytes.TrimSpace(group))
		}
	}
	return ""
}
