package messaging

import "strings"

// This file is the single authority for subject naming. Constructing a subject
// anywhere else eventually produces a namespace nobody can filter on.

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

// Wildcards for subscribing.
const (
	// PatternAllOperations matches every operation lifecycle event.
	PatternAllOperations = "mcp.operation.>"
	// PatternAllPlugins matches every plugin-domain event.
	PatternAllPlugins = "mcp.plugin.>"
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

// PluginPattern matches every event from one plugin.
func PluginPattern(plugin string) string {
	return pluginPrefix + plugin + ".>"
}

// SubjectForState maps a terminal or transitional state to its subject, so
// that the state machine and the subject taxonomy cannot drift apart.
func SubjectForState(state string) string {
	switch state {
	case "pending_approval":
		return SubjectOperationProposed
	case "approved":
		return SubjectOperationApproved
	case "rejected":
		return SubjectOperationRejected
	case "cancelled":
		return SubjectOperationCancelled
	case "expired":
		return SubjectOperationExpired
	case "executing":
		return SubjectOperationExecuting
	case "succeeded":
		return SubjectOperationSucceeded
	case "failed":
		return SubjectOperationFailed
	case "indeterminate":
		return SubjectOperationIndeterminate
	default:
		return ""
	}
}
