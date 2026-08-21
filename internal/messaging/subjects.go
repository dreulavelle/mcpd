package messaging

import "strings"

// This file is the single authority for subject naming. Constructing a subject
// anywhere else eventually produces a namespace nobody can filter on.
//
// Operations map their own states to subjects, in operations.subjectFor. That
// duplication is deliberate and documented there: the domain does not depend
// on the transport layer. A copy of the mapping lived here too and had no
// callers, which made it the version free to drift.

// Operation lifecycle subjects.
const (
	SubjectOperationProposed      = "mcp.operation.proposed"
	SubjectOperationApproved      = "mcp.operation.approved"
	SubjectOperationRejected      = "mcp.operation.rejected"
	SubjectOperationCancelled     = "mcp.operation.cancelled"
	SubjectOperationExpired       = "mcp.operation.expired"
	SubjectOperationExecuting     = "mcp.operation.executing"
	SubjectOperationSucceeded     = "mcp.operation.succeeded"
	SubjectOperationFailed        = "mcp.operation.failed"
	SubjectOperationIndeterminate = "mcp.operation.indeterminate"
)

// pluginPrefix is the namespace a plugin's events live under.
const pluginPrefix = "mcp.plugin."

// PluginSubject builds a plugin-domain subject.
//
// The host binds the prefix rather than letting a plugin supply a full
// subject, so a plugin cannot publish into another plugin's namespace or forge
// an operation lifecycle event.
func PluginSubject(plugin, suffix string) string {
	return pluginPrefix + plugin + "." + strings.TrimPrefix(suffix, ".")
}
