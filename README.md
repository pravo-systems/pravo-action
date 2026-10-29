# pravo-action

Runs [pravo](https://github.com/pravo-systems/pravo) in a GitHub Actions workflow: `plan`, `apply`, `drift` and `discover`, reported on GitHub.

```yaml
permissions:
  contents: read
  pull-requests: write
  issues: write
jobs:
  plan:
    if: github.event_name == 'pull_request'
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: pravo-systems/pravo-action@v1
        with:
          command: plan
          working-directory: app
```

- `plan` stores the plan, keeps one comment on the pull request up to date, and sets the outputs `plan-id`, `has-changes`, `exit-code` and `report`. A plan that a policy refuses is commented and fails the job.
- `apply` takes `plan-id`, and turns the exit codes 3, 4 and 5 into a line that says what to do.
- `drift` and `discover` keep one open issue up to date with what they find and close it when nothing is left. They pass unless `fail-on-findings` is `true`.

The action installs pravo with [setup-pravo](https://github.com/pravo-systems/setup-pravo). Set `version` to pin it, or `install: false` after your own install step. The pravo it runs must have `--markdown` (0.1.0 or newer).

The credentials of the providers and the state are the workflow's: set them in `env`, or log in to the cloud in an earlier step. A review on GitHub is not an approval that pravo counts; see "Run pravo from a workflow" in the pravo docs.

See `action.yml` for every input.
