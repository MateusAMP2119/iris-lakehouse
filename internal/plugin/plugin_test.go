package plugin

import (
	"strings"
	"testing"
	"time"
)

// validManifest returns a well-formed tool manifest document, with the given
// binaries block spliced in.
func validManifest(binaries string) string {
	return `name: smtp-send
version: "1.0"
kind: tool
verbs:
  send:
    timeout: 30s
binaries:
` + binaries
}

const testDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestParseManifest(t *testing.T) {
	binaries := `  darwin/arm64:
    url: https://example.com/smtp-send
    sha256: ` + testDigest + "\n"

	tests := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{name: "valid tool", doc: validManifest(binaries)},
		{name: "unknown field", doc: validManifest(binaries) + "surprise: true\n", wantErr: "unknown field"},
		{name: "bad name", doc: strings.Replace(validManifest(binaries), "smtp-send", "SMTP send", 1), wantErr: "name"},
		{name: "missing kind", doc: strings.Replace(validManifest(binaries), "kind: tool\n", "", 1), wantErr: `missing required field "kind"`},
		{name: "unknown kind", doc: strings.Replace(validManifest(binaries), "kind: tool", "kind: daemon", 1), wantErr: "unknown kind"},
		{name: "tool without verbs", doc: strings.Replace(validManifest(binaries), "verbs:\n  send:\n    timeout: 30s\n", "", 1), wantErr: "declares no verbs"},
		{name: "bad verb timeout", doc: strings.Replace(validManifest(binaries), "30s", "soon", 1), wantErr: "not a duration"},
		{name: "negative verb timeout", doc: strings.Replace(validManifest(binaries), "30s", "-1s", 1), wantErr: "must be positive"},
		{name: "no binaries", doc: strings.Replace(validManifest(binaries), binaries, "  {}\n", 1), wantErr: "pins no binaries"},
		{name: "bad platform key", doc: validManifest(strings.Replace(binaries, "darwin/arm64", "darwin-arm64", 1)), wantErr: "not goos/goarch"},
		{name: "missing url", doc: validManifest(strings.Replace(binaries, "url: https://example.com/smtp-send", `url: ""`, 1)), wantErr: "has no url"},
		{name: "bad sha256", doc: validManifest(strings.Replace(binaries, testDigest, "abc123", 1)), wantErr: "not a hex sha256"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := ParseManifest([]byte(tt.doc))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ParseManifest: %v", err)
				}
				if m.Name != "smtp-send" || m.Version != "1.0" || m.Kind != KindTool {
					t.Fatalf("ParseManifest identity = %+v", m)
				}
				if got := m.Verbs["send"].Duration(); got != 30*time.Second {
					t.Fatalf("verb timeout = %v, want 30s", got)
				}
				return
			}
			if err == nil {
				t.Fatalf("ParseManifest succeeded, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ParseManifest error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestVerbDurationDefault(t *testing.T) {
	if got := (Verb{}).Duration(); got != DefaultVerbTimeout {
		t.Fatalf("zero verb Duration = %v, want %v", got, DefaultVerbTimeout)
	}
}

func TestParseRef(t *testing.T) {
	tests := []struct {
		in      string
		want    Ref
		wantErr bool
	}{
		{in: "smtp-send@1.0", want: Ref{Name: "smtp-send", Version: "1.0"}},
		{in: "lightpanda@0.4-rc1", want: Ref{Name: "lightpanda", Version: "0.4-rc1"}},
		{in: "no-version", wantErr: true},
		{in: "Bad Name@1.0", wantErr: true},
		{in: "name@", wantErr: true},
		{in: "name@ver/sion", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseRef(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseRef(%q) succeeded, want error", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseRef(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("ParseRef(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
			if got.String() != tt.in {
				t.Fatalf("Ref.String() = %q, want %q", got.String(), tt.in)
			}
		})
	}
}
