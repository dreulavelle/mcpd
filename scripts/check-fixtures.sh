#!/bin/sh
# Refuses committed text that looks like it came from a real deployment.
#
# mcpd runs on somebody else's hardware, and its tests and docs are written
# against real integrations. That is how a customer's name, a Bandwidth account
# id and an internal hostname ended up in the public history once, and history
# had to be rewritten to get them out. This is the check that would have
# refused the commit.
#
# Two kinds of rule. The generic ones need no configuration: a North American
# phone number outside the reserved 555 exchange, and a hostname on a private
# suffix that is not an obvious placeholder. The specific one is a denylist of
# your own names and identifiers, kept OUTSIDE the repository so the list is not
# itself a leak: point MCPD_SENSITIVE_TERMS at a file with one term per line.
#
# A line that trips a generic rule for a legitimate reason -- a CSV of byte
# counts that happens to be ten digits -- carries the marker `fixture-ok`
# with a word about why.
#
# Usage: check-fixtures.sh [file ...]   (no arguments: every tracked file)
set -u

if [ "$#" -gt 0 ]; then
    files=$(printf '%s\n' "$@")
else
    files=$(git ls-files)
fi

# Only text a person wrote or captured. Bundles, lockfiles and the public
# catalogue captures are somebody else's data, not this deployment's.
files=$(printf '%s\n' "$files" | grep -v -E \
    '^(web/node_modules/|web/package-lock\.json|internal/admin/dist/|internal/registry/testdata/|CHANGELOG\.md$)' \
    | grep -E '\.(go|md|ts|tsx|sql|yaml|yml|json|txt|sh)$' || true)
[ -z "$files" ] && exit 0

status=0
report() {
    # $1 rule, then the grep output.
    if [ -n "$2" ]; then
        echo "check-fixtures: $1"
        printf '%s\n' "$2" | sed 's/^/  /'
        status=1
    fi
}

# NANP numbers: ten digits with a real area code and exchange, or eleven with a
# leading 1. 555 is reserved for fiction and is what fixtures should use.
# Matched one number at a time rather than one line at a time, so a fictional
# number on the same line as a real one does not excuse it.
phones=$(printf '%s\n' "$files" | xargs grep -H -n -o -E '(^|[^0-9A-Za-z_])1?[2-9][0-9]{2}[2-9][0-9]{2}[0-9]{4}([^0-9A-Za-z_]|$)' 2>/dev/null \
    | grep -v -E ':[^0-9]*1?(555[0-9]{7}|[2-9][0-9]{2}555[0-9]{4})' || true)
# The marker is on the line, not in the match, so it is looked up separately.
phones=$(printf '%s\n' "$phones" | while IFS= read -r hit; do
    [ -z "$hit" ] && continue
    f=${hit%%:*}; rest=${hit#*:}; n=${rest%%:*}
    sed -n "${n}p" "$f" | grep -q 'fixture-ok' || printf '%s\n' "$hit"
done)
report "a phone number that is not in the reserved 555 exchange (use 555-01xx, or mark the line fixture-ok and say why):" "$phones"

# Hostnames on a private suffix. A generic first label -- nas, pbx, sso, the
# name of the product -- is a placeholder; anything else on .local or .lan is a
# real machine's name. Lowercase only, so a Go field called Name.Local is not a
# hostname.
hosts=$(printf '%s\n' "$files" | xargs grep -H -n -E '(^|[^A-Za-z0-9._-])[a-z0-9-]+(\.[a-z0-9-]+)*\.(local|lan|intranet)([^A-Za-z0-9.-]|$)' 2>/dev/null \
    | grep -v -E '(^|[^A-Za-z0-9._-])(mcpd|nas|pbx|sso|host|server|router|switch|printer|plugin|example|acme|globex|cnmaestro|observium|graylog|bookstack)(\.[a-z0-9-]+)*\.(local|lan|intranet)' \
    | grep -v 'fixture-ok' || true)
report "a hostname on a private suffix (name a placeholder such as nas.example instead):" "$hosts"

# Commit trailers naming a machine rather than a person belong nowhere.
machines=$(printf '%s\n' "$files" | xargs grep -H -n -E 'Co-authored-by:.*@[^ >]*\.(local|lan)' 2>/dev/null || true)
report "a co-author trailer with a machine address:" "$machines"

# The denylist, when there is one. Case-insensitive, fixed strings.
if [ -n "${MCPD_SENSITIVE_TERMS:-}" ] && [ -f "$MCPD_SENSITIVE_TERMS" ]; then
    terms=$(grep -v -E '^\s*(#|$)' "$MCPD_SENSITIVE_TERMS" || true)
    if [ -n "$terms" ]; then
        found=$(printf '%s\n' "$files" | xargs grep -H -n -i -F -f "$MCPD_SENSITIVE_TERMS" 2>/dev/null || true)
        report "a term from $MCPD_SENSITIVE_TERMS:" "$found"
    fi
fi

exit $status
