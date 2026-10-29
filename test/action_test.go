// Package test tests run.sh with a pravo and a gh that
// record what they are asked and answer what a test says.
package test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const stubPravo = `#!/bin/sh
echo "$*" >> "$STUB_DIR/pravo.log"
if [ "$1" = plan ] && [ "$2" = --help ]; then
	[ -n "$STUB_NO_MARKDOWN" ] || echo "      --markdown   print the plan as Markdown"
	exit 0
fi
case "$*" in
*"discover --all"*) exit 0 ;;
esac
[ -f "$STUB_DIR/stdout.md" ] && cat "$STUB_DIR/stdout.md"
echo "stderr of pravo" >&2
exit "${STUB_RC:-0}"
`

const stubGH = `#!/bin/sh
echo "$*" >> "$STUB_DIR/gh.log"
case "$1 $2" in
"api repos/o/r/issues/7/comments")
	if [ -f "$STUB_DIR/comments.json" ]; then cat "$STUB_DIR/comments.json"; else echo "[]"; fi ;;
"issue list")
	[ -f "$STUB_DIR/issue" ] && cat "$STUB_DIR/issue" ;;
esac
for a in "$@"; do
	case "$a" in
	body=@*) cp "${a#body=@}" "$STUB_DIR/sent.md" ;;
	esac
done
while [ $# -gt 0 ]; do
	[ "$1" = --body-file ] && cp "$2" "$STUB_DIR/sent.md"
	shift
done
exit 0
`

type result struct {
	code    int
	out     string
	pravo   string
	gh      string
	outputs string
	summary string
	sent    string
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

// run runs the script with the inputs in env. files are written to the stub
// directory first: stdout.md, comments.json and issue.
func run(t *testing.T, env map[string]string, files map[string]string) result {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"pravo": stubPravo, "gh": stubGH} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	event := filepath.Join(dir, "event.json")
	if err := os.WriteFile(event, []byte(`{"pull_request":{"number":7}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("../run.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script)
	cmd.Env = []string{
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"HOME=" + dir,
		"STUB_DIR=" + dir,
		"RUNNER_TEMP=" + dir,
		"GITHUB_OUTPUT=" + filepath.Join(dir, "output"),
		"GITHUB_STEP_SUMMARY=" + filepath.Join(dir, "summary"),
		"GITHUB_EVENT_PATH=" + event,
		"GITHUB_REPOSITORY=o/r",
		"GITHUB_SHA=0123456789abcdef",
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return result{code: code, out: string(out), pravo: read(t, filepath.Join(dir, "pravo.log")), gh: read(t, filepath.Join(dir, "gh.log")),
		outputs: read(t, filepath.Join(dir, "output")), summary: read(t, filepath.Join(dir, "summary")), sent: read(t, filepath.Join(dir, "sent.md"))}
}

func has(t *testing.T, what, text string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(text, w) {
			t.Errorf("%s lacks %q:\n%s", what, w, text)
		}
	}
}

func lacks(t *testing.T, what, text string, nots ...string) {
	t.Helper()
	for _, n := range nots {
		if strings.Contains(text, n) {
			t.Errorf("%s has %q:\n%s", what, n, text)
		}
	}
}

const planReport = "### pravo plan: 1 to add, 0 to change, 0 to destroy\n\n| Action | Resource | Type | Details |\n|---|---|---|---|\n| create | `a` | `t` |  |\n\nStored as `plan-0a1b2c3d`, removed on 2026-11-05 unless it is applied.\n"

func TestPlanCommentsTheStoredPlan(t *testing.T) {
	r := run(t, map[string]string{
		"INPUT_COMMAND": "plan", "INPUT_WORKING_DIRECTORY": "infra", "INPUT_ACTOR": "bob", "INPUT_REASON": "why",
		"INPUT_TAGS": "env=prod\nregion=eu\n", "INPUT_TTL": "36h", "INPUT_ARGS": "--refresh=false",
	}, map[string]string{"stdout.md": planReport})
	if r.code != 0 {
		t.Fatalf("exit %d:\n%s", r.code, r.out)
	}
	has(t, "the pravo calls", r.pravo, "-C infra --actor bob --reason why -t env=prod -t region=eu plan --markdown --store --ttl 36h --refresh=false")
	has(t, "the outputs", r.outputs, "plan-id=plan-0a1b2c3d", "has-changes=true", "exit-code=0")
	has(t, "the gh calls", r.gh, "pr comment 7 --body-file")
	has(t, "the comment", r.sent, "<!-- pravo-action:plan:infra -->", "### pravo plan: 1 to add")
	has(t, "the summary", r.summary, "Stored as `plan-0a1b2c3d`")
}

func TestPlanWithoutStoreDropsTheTTL(t *testing.T) {
	r := run(t, map[string]string{"INPUT_COMMAND": "plan", "INPUT_STORE": "false", "INPUT_TTL": "36h"}, map[string]string{"stdout.md": planReport})
	if r.code != 0 {
		t.Fatalf("exit %d:\n%s", r.code, r.out)
	}
	has(t, "the pravo calls", r.pravo, "plan --markdown")
	lacks(t, "the pravo calls", r.pravo, "--ttl", "--store")
}

func TestPlanUpdatesItsOwnComment(t *testing.T) {
	marker := "<!-- pravo-action:plan:. -->"
	comments := `[{"id":11,"body":"hello"},{"id":55,"body":"` + marker + `\n### old"}]`
	r := run(t, map[string]string{"INPUT_COMMAND": "plan"}, map[string]string{"stdout.md": planReport, "comments.json": comments})
	if r.code != 0 {
		t.Fatalf("exit %d:\n%s", r.code, r.out)
	}
	has(t, "the gh calls", r.gh, "-X PATCH repos/o/r/issues/comments/55")
	lacks(t, "the gh calls", r.gh, "pr comment")
}

func TestPlanThatAPolicyRefusesIsCommentedAndFails(t *testing.T) {
	report := "### pravo plan: 0 to add, 0 to change, 1 to destroy\n\n**Refused by policy.** no_destroy\n"
	r := run(t, map[string]string{"INPUT_COMMAND": "plan", "STUB_RC": "5"}, map[string]string{"stdout.md": report})
	if r.code != 5 {
		t.Fatalf("exit %d, want 5:\n%s", r.code, r.out)
	}
	has(t, "the gh calls", r.gh, "pr comment 7")
	has(t, "the outputs", r.outputs, "exit-code=5")
	has(t, "the log", r.out, "::error::a policy refused the plan")
}

func TestPlanWithoutChangesAndWithoutAComment(t *testing.T) {
	r := run(t, map[string]string{"INPUT_COMMAND": "plan", "INPUT_COMMENT": "false", "INPUT_STORE": "false"},
		map[string]string{"stdout.md": "### pravo plan: no changes\n"})
	if r.code != 0 {
		t.Fatalf("exit %d:\n%s", r.code, r.out)
	}
	has(t, "the outputs", r.outputs, "has-changes=false")
	has(t, "the pravo calls", r.pravo, "plan --markdown")
	lacks(t, "the pravo calls", r.pravo, "--store")
	if r.gh != "" {
		t.Errorf("gh was called:\n%s", r.gh)
	}
}

func TestPlanErrorIsNotCommented(t *testing.T) {
	r := run(t, map[string]string{"INPUT_COMMAND": "plan", "STUB_RC": "6"}, nil)
	if r.code != 6 || r.gh != "" {
		t.Errorf("exit %d, gh calls %q:\n%s", r.code, r.gh, r.out)
	}
	has(t, "the log", r.out, "::error::pravo plan exited 6", "stderr of pravo")
}

func TestAnOldPravoIsRefusedWithAReason(t *testing.T) {
	r := run(t, map[string]string{"INPUT_COMMAND": "plan", "STUB_NO_MARKDOWN": "1"}, nil)
	if r.code != 1 {
		t.Fatalf("exit %d:\n%s", r.code, r.out)
	}
	has(t, "the log", r.out, "has no --markdown")
}

func TestApplyNamesWhatWentWrong(t *testing.T) {
	r := run(t, map[string]string{"INPUT_COMMAND": "apply", "INPUT_PLAN_ID": "plan-0a1b2c3d"}, nil)
	if r.code != 0 {
		t.Fatalf("exit %d:\n%s", r.code, r.out)
	}
	has(t, "the pravo calls", r.pravo, "-C . apply plan-0a1b2c3d")
	has(t, "the summary", r.summary, "Applied `plan-0a1b2c3d`.")

	r = run(t, map[string]string{"INPUT_COMMAND": "apply", "INPUT_PLAN_ID": "plan-0a1b2c3d", "STUB_RC": "3"}, nil)
	if r.code != 3 {
		t.Fatalf("exit %d, want 3:\n%s", r.code, r.out)
	}
	has(t, "the log", r.out, "is stale")

	if r = run(t, map[string]string{"INPUT_COMMAND": "apply"}, nil); r.code != 1 || !strings.Contains(r.out, "apply needs plan-id") {
		t.Errorf("apply without an id exited %d:\n%s", r.code, r.out)
	}
}

const driftReport = "### pravo drift: 1 of 2 resources changed outside pravo\n\n| Resource | What happened |\n|---|---|\n| `lost` | the object does not exist |\n"

func TestDriftKeepsOneIssue(t *testing.T) {
	// Drift and no issue: one is opened, and the job passes unless it is asked to fail.
	r := run(t, map[string]string{"INPUT_COMMAND": "drift", "STUB_RC": "2", "INPUT_DUE_LIMIT": "100"}, map[string]string{"stdout.md": driftReport})
	if r.code != 0 {
		t.Fatalf("exit %d:\n%s", r.code, r.out)
	}
	has(t, "the pravo calls", r.pravo, "drift --markdown --due --limit 100")
	has(t, "the gh calls", r.gh, "label create pravo-drift", "issue create --title Drift found by pravo --label pravo-drift --body-file")
	has(t, "the outputs", r.outputs, "exit-code=2")
	has(t, "the issue", r.sent, "`lost`")

	// With an open issue it is edited.
	r = run(t, map[string]string{"INPUT_COMMAND": "drift", "STUB_RC": "2", "INPUT_PROPOSE": "true", "INPUT_FAIL_ON_FINDINGS": "true"},
		map[string]string{"stdout.md": driftReport, "issue": "12\n"})
	if r.code != 2 {
		t.Fatalf("exit %d, want 2:\n%s", r.code, r.out)
	}
	has(t, "the pravo calls", r.pravo, "drift --markdown --plan --store")
	has(t, "the gh calls", r.gh, "issue edit 12 --body-file")
	lacks(t, "the gh calls", r.gh, "issue create")

	// No drift closes the issue.
	r = run(t, map[string]string{"INPUT_COMMAND": "drift"}, map[string]string{"stdout.md": "### pravo drift: no drift\n", "issue": "12\n"})
	if r.code != 0 {
		t.Fatalf("exit %d:\n%s", r.code, r.out)
	}
	has(t, "the gh calls", r.gh, "issue comment 12", "issue close 12")
}

func TestDiscoverRunsThenShows(t *testing.T) {
	r := run(t, map[string]string{"INPUT_COMMAND": "discover", "STUB_RC": "2"}, map[string]string{"stdout.md": "### pravo discover vms: 3 unmanaged\n"})
	if r.code != 0 {
		t.Fatalf("exit %d:\n%s", r.code, r.out)
	}
	has(t, "the pravo calls", r.pravo, "-C . discover --all", "-C . discover show --markdown --exit-code")
	has(t, "the gh calls", r.gh, "label create pravo-discover", "issue create --title Unmanaged objects found by pravo --label pravo-discover")
}

func TestUnknownCommand(t *testing.T) {
	r := run(t, map[string]string{"INPUT_COMMAND": "destroy"}, nil)
	if r.code != 1 || !strings.Contains(r.out, "command must be plan, apply, drift or discover") {
		t.Errorf("exit %d:\n%s", r.code, r.out)
	}
}
