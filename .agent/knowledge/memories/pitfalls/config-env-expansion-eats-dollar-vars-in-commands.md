---
name: config-env-expansion-eats-dollar-vars-in-commands
description: config.yaml values are env-var-expanded at load — a shell $var inside a quality-gate command (e.g. `for f in ...; do bash -n $f; done`) is substituted with an empty string before the shell ever runs, silently neutering the gate; only a WARN at load hints at it
type: pitfall
---

# config.yaml env-expands `$var` — shell variables in gate commands are silently blanked

**What happened (2026-08-03, aws-infrastructure-pilot onboarding):** the new
project entry got a per-project quality gate

```yaml
command: bash -c 'for f in scripts/*.sh; do bash -n $f || exit 1; done'
```

Running `pilot version` (which loads config) printed:

```
WARN: config: environment variable "f" at line 500 is unset or empty: command: bash -c 'for f in ...'
```

The config loader expands `$f` as an **environment variable** before the
command string reaches the shell. `$f` → empty → the gate becomes
`bash -n || exit 1` per iteration — it checks nothing and passes. The gate
would have reported green forever.

**Why it's easy to miss:** the WARN only appears on config load (daemon
start / any `pilot` CLI invocation on the box) and the gate itself still
exits 0. Nothing fails; coverage just disappears.

**How to apply:**
- Never use `$anything` in `command:` strings in `~/.pilot/config.yaml`
  unless you *want* load-time env substitution.
- Rewrite shell loops without variables, e.g.
  `bash -c "ls scripts/*.sh | xargs -n1 bash -n"` (xargs exits 123 on any
  child failure — gate still fails correctly).
- After any config edit, run `/var/lib/pilot/bin/pilot version` on the box
  and treat **any** `WARN: config:` line as a blocker.

Related: [[workflow-yaml-requires-front-matter]] (same family: config that
loads "fine" but silently does nothing).
