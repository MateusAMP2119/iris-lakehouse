package dispatch

import "strings"

// This file is the run-log framing contract: the line tags a framed (declared
// logs block) capture file carries. The daemon writes framed captures, the API
// naturalizes or filters them, and the CLI renders them; the shared vocabulary
// lives here so all three agree without importing each other.
//
// A framed capture always opens with a LogLineMeta identity header, so the
// first two bytes of the file are the framing marker; a legacy raw capture (the
// pre-declaration default) has no such header and is served byte-for-byte.

// The framed-capture line tags. Every line of a framed capture starts with
// exactly one of these two-byte tags; the rest of the line is the payload.
const (
	// LogLineMeta tags a stamp line: a JSON document the engine writes at the
	// capture's open (run id, pipeline, start) and, under the declared stamp,
	// its close (end, outcome).
	LogLineMeta = "#|"
	// LogLineLog tags one line of the pipeline's free-form stderr log.
	LogLineLog = "L|"
	// LogLineEngineFrame tags one engine-to-pipeline protocol frame (go/row/run).
	LogLineEngineFrame = ">|"
	// LogLinePipelineFrame tags one pipeline-to-engine protocol frame
	// (row/done/error).
	LogLinePipelineFrame = "<|"
)

// FramedCapture reports whether the capture content beginning with head is
// line-framed: a framed capture always opens with the LogLineMeta header.
func FramedCapture(head string) bool { return strings.HasPrefix(head, LogLineMeta) }
