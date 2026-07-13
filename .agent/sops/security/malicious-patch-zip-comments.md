# SOP: Malicious "patch .zip" comments on Pilot issues (social-engineering / supply-chain)

**Category:** security · **Created:** 2026-07-08 · **First incident:** 2026-07-07/08

## Threat pattern

Throwaway GitHub accounts monitor the **public** `qf-studio/pilot` issue tracker and, within ~30 min of a maintainer filing a bug, post a comment that:

- attaches a `*.zip` "patch" on `github.com/user-attachments/files/...`, named to match the bug (`sdk_patch_v2.zip`, `pilot_hydrator_patch.zip`);
- uses a near-identical template: *"Man, that <bug> was such a headache for my workflow. I finally found a way to <plausible fix>. It totally stopped the <symptom> for me."*

Goal: get a human maintainer **or an AI assistant** to download + apply the zip — malicious payload disguised as the fix for the exact bug.

**Confirmed 2026-07:** `nadowabube76` → #4050 (`sdk_patch_v2.zip`); `vodacusedo` → #4041 (`pilot_hydrator_patch.zip`). Both created hours before commenting, 0 repos / 0 followers, not collaborators. (`hiSandog` — an old real account posting a benign technical comment — is NOT part of this; don't over-trigger on every external commenter.)

## Why Pilot is architecturally immune (verified 2026-07-08)

1. **The executor never ingests issue comments.** The task prompt `Description` = `issue.Title + issue.Body` only (`cmd/pilot/handlers.go:254` legacy / `ev.Body` SDK path). Every `comment` reference in code is Pilot *posting* status, never reading → a comment/attachment cannot reach an execution prompt.
2. **Pilot only acts on `pilot`-labeled issues, and the label needs write access.** Every `pilot` issue was owner-authored; non-collaborators can't label or edit a body. `auto_label_pilot` is vestigial/off.

Net: the payload's only reachable target is a human (or AI assistant) who manually downloads + applies it. Do not be that target.

## Response checklist

1. **Do NOT download or open the zip** on any host. For forensics, use an isolated throwaway VM — never the dev machine or a Pilot worktree.
2. **Contain:** remove the `pilot` label from the targeted issue (halts dispatch). Stop the daemon only if an execution may be mid-flight.
3. **Verify no ingestion / no damage:** disk sweep for the zip name (`find ~/Downloads ~/.pilot /tmp -iname '*patch*'`); confirm no `pilot/GH-<n>` branch, no PR, no worktree, execution row produced 0 files; grep daemon log for the `user-attachments/files/<id>` URL.
4. **Attribution:** `gh api users/<login>` (age, repos, followers) + `gh api repos/qf-studio/pilot/collaborators/<login>` (404 = not a collaborator). Throwaway = created recently, 0/0.
5. **Scope the campaign:** `gh api --paginate repos/qf-studio/pilot/issues/comments` → filter `.body | test("user-attachments/files|\\.zip")`; check newest comments (`sort=created&direction=desc`) since the sweep can miss the latest page.
6. **Report + remove:** report the accounts + attachments to GitHub abuse (they take down malware org-wide and can suspend). Then hide (minimize as `spam`) or delete the comments. GitHub's scanner may auto-remove them first (nadowabube76's #4050 comment 404'd on its own).
7. **Re-enable safely:** because comments aren't ingested, restart the daemon + re-add the `pilot` label — the pending real fix proceeds from the body. (#4041's real fix shipped as PR #4043 while the zip sat ignored.)

## Hardening options (defense-in-depth, not required)

- GitHub → Settings → Moderation → **Limit interactions** to prior contributors, or **Interaction limits** during active campaigns.
- Make the executor's comment-blindness an **explicit tested invariant** (assert the prompt builder never reads comments/attachments).
- Consider whether public bug reports hand attackers a roadmap; weigh private issues for security-sensitive defects.

## Golden rule

A `.zip` "fix" from a non-collaborator on an issue you just filed is a payload, not a contribution. Implement fixes **from the issue spec you authored**, never from an attached patch.
