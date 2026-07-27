package dispatch

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

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

// MinLevelRank resolves a minimum-level name (any case; "warning" accepted)
// to its rank; empty or unknown keeps all (rank 0).
func MinLevelRank(name string) int {
	if code, ok := levelTokens[strings.ToUpper(name)]; ok {
		return LevelRank(code)
	}
	return 0
}

// ParseLogLevel splits one log line into its level code and message. The
// level is whatever the writing logger stated, in any of the shapes native
// loggers emit -- the level lives at the logging call, never hand-written
// into the message:
//
//   - a JSON log line: {"level":"warn","msg":"..."} (slog JSON, log4j JSON
//     layout, pino, python-json-logger; "message" accepted for "msg")
//   - Python logging's default format: "WARNING:logger:message"
//   - a leading DEBUG/INFO/WARN/WARNING/ERROR token, colon optional
//
// A line matching none of these is INFO, unchanged.
func ParseLogLevel(line string) (code, msg string) {
	if code, msg, ok := parseJSONLog(line); ok {
		return code, msg
	}
	// Python logging default: LEVELNAME:loggername:message (no spaces around
	// the colons). The level is stripped; logger name and message stay.
	if head, rest, cut := strings.Cut(line, ":"); cut && !strings.Contains(head, " ") && !strings.HasPrefix(rest, " ") {
		if lvl, ok := levelTokens[head]; ok {
			return lvl, rest
		}
	}
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

// parseJSONLog reads a JSON log line's level and message, reporting whether
// the line is one. Extra scalar fields ride appended as k=v, sorted, so
// structured context survives into the rendered line.
func parseJSONLog(line string) (code, msg string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "{") {
		return "", "", false
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		return "", "", false
	}
	rawLevel, has := doc["level"]
	if !has {
		rawLevel, has = doc["levelname"]
	}
	levelStr, isStr := rawLevel.(string)
	if !has || !isStr {
		return "", "", false
	}
	lvl, known := levelTokens[strings.ToUpper(strings.TrimSpace(levelStr))]
	if !known {
		return "", "", false
	}
	message, _ := doc["msg"].(string)
	if message == "" {
		message, _ = doc["message"].(string)
	}
	var extras []string
	for k, v := range doc {
		switch k {
		case "level", "levelname", "msg", "message", "time", "timestamp", "ts":
			continue
		}
		switch v.(type) {
		case string, float64, bool:
			extras = append(extras, fmt.Sprintf("%s=%v", k, v))
		}
	}
	sort.Strings(extras)
	if len(extras) > 0 {
		message = strings.TrimSpace(message + " " + strings.Join(extras, " "))
	}
	return lvl, message, true
}
