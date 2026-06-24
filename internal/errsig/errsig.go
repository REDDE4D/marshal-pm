// Package errsig turns raw stderr lines into deduplicated error signatures.
// It is pure: no I/O, no DB, no wall-clock — all timestamps are passed in.
package errsig

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// infoWarnMarkers, when present and no error marker is present, mark a line as
// NOT an error (info/warn/debug on stderr). Lowercased substring match.
var infoWarnMarkers = []string{
	"level=info", "level=debug", "level=trace", "level=warn", "level=warning",
	"[info]", "[debug]", "[trace]", "[warn]", "[warning]", "[notice]",
}

// reLevelPrefix matches lines where a log level word appears at the start
// (optionally preceded by whitespace or a bracket). Used to catch bare-word
// level prefixes like "DEBUG cache miss" or "INFO listening" without
// false-matching level words buried in the middle of a message.
var reLevelPrefix = regexp.MustCompile(`^\s*\[?(?:debug|info|notice|trace|warn|warning)\b`)

// errorMarkers force a line to count as an error even if it also matches an
// info/warn token. Lowercased substring match.
var errorMarkers = []string{
	"error", "fatal", "panic", "exception",
	"traceback (most recent call last)", "level=error",
}

// IsError reports whether a stderr line should be grouped as an error. Explicit
// error markers always count; recognized info/warn/debug markers are excluded;
// everything else on stderr counts (stderr default).
func IsError(text string) bool {
	l := strings.ToLower(text)
	for _, m := range errorMarkers {
		if strings.Contains(l, m) {
			return true
		}
	}
	for _, m := range infoWarnMarkers {
		if strings.Contains(l, m) {
			return false
		}
	}
	if reLevelPrefix.MatchString(l) {
		return false
	}
	return true
}

var (
	reTimestamp = regexp.MustCompile(`^\s*\[?\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:?\d{2})?\]?\s*`)
	reTimeOnly  = regexp.MustCompile(`^\s*\[?\d{2}:\d{2}:\d{2}(\.\d+)?\]?\s*`)
	reQuoted    = regexp.MustCompile("\"[^\"]*\"|'[^']*'|`[^`]*`")
	reUUID      = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	reHex       = regexp.MustCompile(`0x[0-9a-fA-F]+|\b[0-9a-fA-F]{12,}\b`)
	reAddr      = regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}(:\d+)?\b`)
	rePath      = regexp.MustCompile(`(?:[A-Za-z]:\\|/|\./)[^\s:]+(?::\d+)?`)
	reNum       = regexp.MustCompile(`(?i)\b\d+(\.\d+)?(ns|us|µs|ms|s|m|h|ki?b|mi?b|gi?b|b)?\b`)
	reSpace     = regexp.MustCompile(`\s+`)
)

// Normalize canonicalizes a message for grouping: strip leading timestamp,
// then replace quoted strings, UUIDs, hex/addresses, IPs, paths, and numbers
// with placeholders; collapse whitespace; lowercase.
func Normalize(text string) string {
	s := reTimestamp.ReplaceAllString(text, "")
	s = reTimeOnly.ReplaceAllString(s, "")
	s = reQuoted.ReplaceAllString(s, "<str>")
	s = reUUID.ReplaceAllString(s, "<uuid>")
	s = reHex.ReplaceAllString(s, "<hex>")
	s = reAddr.ReplaceAllString(s, "<addr>")
	s = rePath.ReplaceAllString(s, "<path>")
	s = reNum.ReplaceAllString(s, "<num>")
	s = reSpace.ReplaceAllString(s, " ")
	return strings.ToLower(strings.TrimSpace(s))
}

// Signature is a stable 12-hex-char id for a normalized message.
func Signature(text string) string {
	sum := sha256.Sum256([]byte(Normalize(text)))
	return hex.EncodeToString(sum[:])[:12]
}

var (
	reSrcPy = regexp.MustCompile(`File "([^"]+)", line (\d+)`)
	reSrcGo = regexp.MustCompile(`([\w./\\-]+\.(?:go|py|js|ts|rb|rs|java|c|cc|cpp|h|hpp|php|cs|kt|swift|scala|ex|exs)):(\d+)`)
	reSrcAt = regexp.MustCompile(`\bat ([\w./\\-]+):(\d+)`)
)

// Source returns a best-effort "file:line" from the error line plus a few
// following lines (a stack trace), or "" when nothing recognizable is found.
func Source(window []string) string {
	for _, ln := range window {
		if m := reSrcPy.FindStringSubmatch(ln); m != nil {
			return baseName(m[1]) + ":" + m[2]
		}
		if m := reSrcGo.FindStringSubmatch(ln); m != nil {
			return baseName(m[1]) + ":" + m[2]
		}
		if m := reSrcAt.FindStringSubmatch(ln); m != nil {
			return baseName(m[1]) + ":" + m[2]
		}
	}
	return ""
}

// isTraceHeader reports whether a line begins a multi-line stack trace whose
// source frame appears on following lines (Go panics, Python tracebacks).
func isTraceHeader(text string) bool {
	l := strings.ToLower(text)
	return strings.HasPrefix(l, "panic:") ||
		strings.HasPrefix(l, "goroutine ") ||
		strings.HasPrefix(l, "fatal error:") ||
		strings.Contains(l, "traceback (most recent call last)") ||
		strings.Contains(l, "exception")
}

// baseName returns the last path element (handles / and \).
func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}
