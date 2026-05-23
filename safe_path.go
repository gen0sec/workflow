package workflow

import (
	"fmt"
	"regexp"
)

// safeExecutionIDPattern matches the execution-ID format we accept
// when interpolating into a filesystem path. Letters, digits, dot,
// underscore, hyphen — no path separators, no NUL. The first
// character must be alphanumeric so the pattern can't match "..",
// "." or any hidden-file form (".foo"). Length capped at 128. Long
// enough for the engine's own "exec_<26-char base32>" format and
// the consumer-supplied IDs used by gen0sec/workflow-service
// ("ui-<nanos>", "monitoring-<...>", etc).
//
// Go's filepath.Join does NOT clean ".." segments out, so a path
// like filepath.Join(baseDir, "../etc/passwd") happily escapes
// baseDir. The FileCheckpointer and FileActivityLogger are
// development helpers (production deployments use a Postgres store)
// but we still validate so a misconfigured consumer can't be talked
// into writing arbitrary paths.
var safeExecutionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// validateExecutionID returns an error if executionID is unsuitable
// for use as a filesystem path component. The check is intentionally
// strict — see safeExecutionIDPattern.
func validateExecutionID(executionID string) error {
	if !safeExecutionIDPattern.MatchString(executionID) {
		return fmt.Errorf("invalid execution id (must match %s)", safeExecutionIDPattern.String())
	}
	return nil
}
