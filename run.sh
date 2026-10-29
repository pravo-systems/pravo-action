#!/bin/bash
# Runs one pravo command for the pravo action and reports it on GitHub. The
# inputs arrive as INPUT_* variables. See action.yml.
set -u

export GH_REPO=${GH_REPO:-${GITHUB_REPOSITORY:-}}

command=${INPUT_COMMAND:-}
dir=${INPUT_WORKING_DIRECTORY:-.}
work=${RUNNER_TEMP:-${TMPDIR:-/tmp}}/pravo-action
mkdir -p "$work"
report=$work/report.md
errlog=$work/stderr.txt
: > "$report"

output() { [ -n "${GITHUB_OUTPUT:-}" ] && printf '%s=%s\n' "$1" "$2" >> "$GITHUB_OUTPUT"; return 0; }
summary() { [ -n "${GITHUB_STEP_SUMMARY:-}" ] && cat "$1" >> "$GITHUB_STEP_SUMMARY"; return 0; }
warn() { echo "::warning::$*"; }
fail() { echo "::error::$*"; output exit-code 1; exit 1; }

common=(-C "$dir")
[ -n "${INPUT_ACTOR:-}" ] && common+=(--actor "$INPUT_ACTOR")
[ -n "${INPUT_REASON:-}" ] && common+=(--reason "$INPUT_REASON")
while IFS= read -r tag; do
	tag=${tag//[$'\r']/}
	[ -n "$tag" ] && common+=(-t "$tag")
done <<< "${INPUT_TAGS:-}"
extra=()
# shellcheck disable=SC2206 # the arguments are split on spaces on purpose
[ -n "${INPUT_ARGS:-}" ] && extra=($INPUT_ARGS)

# pravo runs pravo with the common arguments, the command words and the extra
# arguments. Standard output goes to the file named by $1.
pravo_run() {
	local to=$1
	shift
	pravo "${common[@]}" "$@" ${extra[@]+"${extra[@]}"} > "$to" 2> "$errlog"
}

# need_markdown stops with a clear message when the installed pravo is too old.
need_markdown() {
	pravo plan --help 2>&1 | grep -q -- '--markdown' && return 0
	fail "the installed pravo has no --markdown. Name a newer version in the version input, or build pravo from a checkout."
}

pr_number() {
	if [ -n "${INPUT_PR_NUMBER:-}" ]; then echo "$INPUT_PR_NUMBER"; return; fi
	if [ -n "${GITHUB_EVENT_PATH:-}" ] && [ -f "$GITHUB_EVENT_PATH" ]; then
		jq -r '.pull_request.number // empty' "$GITHUB_EVENT_PATH" 2>/dev/null
	fi
}

# sticky_comment keeps one comment on the pull request up to date.
sticky_comment() {
	local pr marker body id
	[ "${INPUT_COMMENT:-true}" = true ] || return 0
	pr=$(pr_number)
	if [ -z "$pr" ]; then
		echo "::notice::no pull request to comment on"
		return 0
	fi
	marker="<!-- pravo-action:plan:$dir -->"
	body=$work/comment.md
	{ echo "$marker"; cat "$report"; } > "$body"
	id=$(gh api "repos/$GITHUB_REPOSITORY/issues/$pr/comments" --paginate 2>/dev/null | jq -r --arg m "$marker" '.[] | select(.body | contains($m)) | .id' | head -n 1)
	if [ -n "$id" ]; then
		gh api -X PATCH "repos/$GITHUB_REPOSITORY/issues/comments/$id" -F "body=@$body" > /dev/null || warn "could not update the comment on #$pr"
	else
		gh pr comment "$pr" --body-file "$body" > /dev/null || warn "could not comment on #$pr"
	fi
}

# upsert_issue keeps one open issue per label: its body is the report while
# something is found, and it is closed when nothing is.
upsert_issue() {
	local label=$1 title=$2 found=$3 n
	[ "${INPUT_COMMENT:-true}" = true ] || return 0
	n=$(gh issue list --label "$label" --state open --json number --jq '.[0].number // empty' 2>/dev/null)
	if [ "$found" = true ]; then
		if [ -n "$n" ]; then
			gh issue edit "$n" --body-file "$report" > /dev/null || warn "could not update issue #$n"
		else
			gh label create "$label" --force > /dev/null 2>&1
			gh issue create --title "$title" --label "$label" --body-file "$report" > /dev/null || warn "could not open the issue"
		fi
	elif [ -n "$n" ]; then
		gh issue comment "$n" --body "Nothing found as of $(printf '%.7s' "${GITHUB_SHA:-}"). Closing." > /dev/null
		gh issue close "$n" > /dev/null || warn "could not close issue #$n"
	fi
}

finish() { # finish RC FOUND: the exit code of the step
	local rc=$1 found=${2:-false}
	output exit-code "$rc"
	output report "$report"
	if [ "$rc" = 2 ] && [ "$found" = true ] && [ "${INPUT_FAIL_ON_FINDINGS:-false}" != true ]; then
		exit 0
	fi
	exit "$rc"
}

case $command in
plan)
	need_markdown
	words=(plan --markdown)
	if [ "${INPUT_STORE:-true}" = true ]; then
		words+=(--store)
		[ -n "${INPUT_TTL:-}" ] && words+=(--ttl "$INPUT_TTL")
	fi
	pravo_run "$report" "${words[@]}"
	rc=$?
	cat "$errlog" >&2
	# 0 is a plan and 5 a plan that a policy refused: both have a report.
	if [ $rc != 0 ] && [ $rc != 5 ]; then
		echo "::error::pravo plan exited $rc"
		output exit-code "$rc"
		exit "$rc"
	fi
	id=$(grep -o 'plan-[0-9a-f]\{8\}' "$report" | head -n 1)
	output plan-id "$id"
	if grep -q '^### [^:]*: no changes' "$report"; then output has-changes false; else output has-changes true; fi
	summary "$report"
	sticky_comment
	[ $rc = 5 ] && echo "::error::a policy refused the plan"
	finish "$rc"
	;;
apply)
	[ -n "${INPUT_PLAN_ID:-}" ] || fail "apply needs plan-id"
	pravo "${common[@]}" apply "$INPUT_PLAN_ID" ${extra[@]+"${extra[@]}"} 2>&1 | tee "$errlog"
	rc=${PIPESTATUS[0]}
	{
		case $rc in
		0) echo "Applied \`$INPUT_PLAN_ID\`." ;;
		3) echo "\`$INPUT_PLAN_ID\` is stale: the state or the specification changed since it was made. Make the plan again." ;;
		4) echo "\`$INPUT_PLAN_ID\` was rejected: a resource is locked or reserved by someone else." ;;
		5) echo "A policy refused \`$INPUT_PLAN_ID\`, or it is waiting for approvals." ;;
		*) echo "\`pravo apply $INPUT_PLAN_ID\` exited $rc." ;;
		esac
	} > "$report"
	summary "$report"
	[ $rc = 0 ] || echo "::error::$(cat "$report")"
	finish "$rc"
	;;
drift)
	need_markdown
	words=(drift --markdown)
	if [ -n "${INPUT_DUE_LIMIT:-}" ]; then
		words+=(--due --limit "$INPUT_DUE_LIMIT")
	elif [ "${INPUT_PROPOSE:-false}" = true ]; then
		words+=(--plan --store)
		[ -n "${INPUT_TTL:-}" ] && words+=(--ttl "$INPUT_TTL")
	fi
	pravo_run "$report" "${words[@]}"
	rc=$?
	cat "$errlog" >&2
	if [ $rc != 0 ] && [ $rc != 2 ]; then
		echo "::error::pravo drift exited $rc"
		output exit-code "$rc"
		exit "$rc"
	fi
	summary "$report"
	found=false
	[ $rc = 2 ] && found=true
	upsert_issue "${INPUT_LABEL:-pravo-drift}" "Drift found by pravo" "$found"
	finish "$rc" "$found"
	;;
discover)
	need_markdown
	pravo_run /dev/null discover --all
	rc=$?
	cat "$errlog" >&2
	if [ $rc != 0 ]; then
		echo "::error::pravo discover exited $rc"
		output exit-code "$rc"
		exit "$rc"
	fi
	pravo_run "$report" discover show --markdown --exit-code
	rc=$?
	cat "$errlog" >&2
	if [ $rc != 0 ] && [ $rc != 2 ]; then
		echo "::error::pravo discover show exited $rc"
		output exit-code "$rc"
		exit "$rc"
	fi
	summary "$report"
	found=false
	[ $rc = 2 ] && found=true
	upsert_issue "${INPUT_LABEL:-pravo-discover}" "Unmanaged objects found by pravo" "$found"
	finish "$rc" "$found"
	;;
*)
	fail "command must be plan, apply, drift or discover, not \"$command\""
	;;
esac
