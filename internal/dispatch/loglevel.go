package dispatch

import "strings"

// This file is the application-log level vocabulary: the leading-token
// convention a pipeline's log lines carry (any language, plain prints), the
// single-char level codes a framed capture stores, and the shared parser --
// the daemon stamps with it, the API filters by it, the CLI renders from it.

// The capture's single-char level codes.
const (
	// LevelDebug marks a DEBUG line.
	LevelDebug = "D"
	// LevelInfo marks an INFO line -- the default for a bare, untagged line.
	LevelInfo = "I"
	// LevelWarn marks a WARN line.
	LevelWarn = "W"
	// LevelError marks an ERROR line.
	LevelError = "E"
)

// levelTokens maps the accepted leading tokens (upper-case, colon optional)
// to their level codes.
var levelTokens = map[string]string{
	"DEBUG": LevelDebug, "INFO": LevelInfo,
	"WARN": LevelWarn, "WARNING": LevelWarn, "ERROR": LevelError,
}

// LevelName renders a level code as its display name (INFO for unknown codes).
func LevelName(code string) string {
	switch code {
	case LevelDebug:
		return "DEBUG"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "INFO"
	}
}

// LevelRank orders level codes for minimum-level filtering (DEBUG < INFO <
// WARN < ERROR); unknown codes rank as INFO.
func LevelRank(code string) int {
	switch code {
	case LevelDebug:
		return 0
	case LevelWarn:
		return 2
	case LevelError:
		return 3
	default:
		return 1
	}
}

// ParseLogLevel splits one log line into its level code and message: a
// leading DEBUG/INFO/WARN/WARNING/ERROR token (colon optional) sets the level
// and is stripped; a bare line is INFO, unchanged.
func ParseLogLevel(line string) (code, msg string) {
	token, rest, cut := strings.Cut(line, " ")
	stripped := strings.TrimSuffix(token, ":")
	if lvl, ok := levelTokens[stripped]; ok {
		if !cut {
			return lvl, ""
		}
		return lvl, rest
	}
	if lvl, ok := levelTokens[strings.TrimSuffix(line, ":")]; ok {
		return lvl, ""
	}
	return LevelInfo, line
}
