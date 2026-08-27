# test(tenantbase): verify the ingress source and give the KMS check a canary, so the isolation gate cannot pass vacuously

**Status**: ✅ SHIPPED + REVIEWED 2026-08-27 — infra#39 → PR#40 merged 11:26Z, **APPROVE-w-notes** (verdict on the PR). Both cannot-fail checks genuinely fixed, proven by live mutation testing (each vacuity mutation killed by a new test). SG check compares source **value** against the control-plane template's own export (cross-template provenance, not the PR#32 self-assertion class; fails closed on 0/2+ matches and `Ref` sources). KMS check gained a `kms:DescribeKey` ARN-validation canary (spec option 2; simulator-evaluates-policy proof carried by `CheckSSMOwnTenantAllowed` through the same path — docs argue this honestly). Zero AWS mutation (DescribeKey read-only; gate stays behind `PILOT_TENANT_BOUNDARY_CHECK`, out of CI). Notes: docs omit the new `kms:DescribeKey` credential requirement · `service/kms` still `// indirect` in go.mod · RunAll partial-summary path untested.
**Created**: 2026-08-27
**Last Updated**: 2026-08-27
**Target repo**: qf-studio/pilot-cloud-infra
**Follows**: qf-studio/pilot-cloud-infra#37 → PR#38 (merged; reviewed REQUEST-CHANGES)

## Context

The tenant-isolation boundary checks are the S5 phase-exit gate — the verification that our customer-facing isolation promise holds before any private-repo customer onboards. PR#38 rebuilt them around asserting configuration rather than attempting access, and that technique change worked: five of seven checks are genuinely non-vacuous, each synthesis check has a paired reject-the-widened-control test, the error-versus-deny distinction is handled correctly, and nothing creates or destroys an AWS resource.

Two checks, however, do not verify the boundary they claim to, and a gate that cannot fail is the specific problem this rework existed to eliminate.

**The security-group check never compares the ingress source.** It records only whether a source-security-group key is present in the rule, not which group it names, and the configured control-plane group is never referenced. A tenant security group whose single rule is TCP on the tenant service port sourced from **another tenant's** group therefore passes every assertion: one group, one rule, correct protocol, correct port, source present, no CIDR. That is exactly the "no rule sourced from another tenant's group" clause the boundary is supposed to enforce. It is verifiable — the synthesized template renders the source as an import of the control-plane group's id.

**The KMS cross-tenant check passes against a bad input.** Policy simulation genuinely cannot evaluate a resource policy for a role principal, and the code and docs are honest about that; the substitute assertion is that the tenant role's identity policy grants no decrypt. But there is no allowed-case counterpart, so a mistyped or nonexistent key ARN produces an implicit deny and reads as success. For the same reason the encryption-context entries are inert, leaving the check structurally insensitive to the tenant boundary. The summary meanwhile advertises that decrypting with another tenant's encryption context is denied, which is not what ran.

## Task

**Compare the ingress source.** Resolve the expected control-plane group from the synthesized template and assert the rule's source matches it. Add a negative test with a rule on the correct port and protocol but sourced from a foreign group — the current widened-group test only adds SSH from anywhere, which the existing rule-count assertion would have caught anyway, so it does not exercise the source comparison at all.

**Give the KMS check something that can fail for the right reason.** Either add a positive counterpart asserting the role is allowed something it must be allowed against the same key, or validate the key ARN before an implicit deny is trusted. The SSM pair is the model here: its allowed case is what makes a wrong ARN or a failed tag substitution fail loudly rather than look like a pass. Retitle the boundary text to describe what is actually asserted.

**Record the residuals honestly** in the boundary documentation, rather than leaving them implied:

- No tenant instance is covered by the metadata-service check. The tenant stack synthesizes no instances — they are created at runtime by the console's resource factory — so only the control-plane instance is asserted, and the instance-disk leg of the promise sits outside this gate.
- The KMS key is shared and scoped by encryption context rather than being per-tenant, the check hard-codes a single key, and the guarantee depends on callers setting that context, which nothing enforces.

**Make the simulation layer testable.** It currently takes a concrete client, so the error-versus-deny mapping and the empty-results guard have no coverage. Introduce a narrow interface and fakes — the harness this replaced had exactly that, and it was lost in the swap.

**Two smaller fixes:** a credential-loading failure currently discards the already-computed synthesis results, so a configuration problem yields no summary at all; and the command exits zero with a skip message when the opt-in variable is unset, which means wiring it into a release gate without that variable would silently pass.

## Acceptance

- The security-group check fails when the single rule is sourced from a group other than the configured control-plane group, proven by a negative test.
- The KMS cross-tenant check cannot pass on a bad or nonexistent key ARN, and its summary text describes what it actually asserts.
- Both residuals above are stated in the boundary documentation.
- The simulation layer has tests covering the error path, the empty-results path, and the deny and allow decisions.
- A credential failure still emits the synthesis results.
- Running the gate without the opt-in variable cannot be mistaken for a pass.
- Synthesis checks stay credential-free and in the ordinary test run; simulation stays opt-in and out of CI; nothing creates or destroys AWS resources.

## Refs

- Pilot issue: https://github.com/qf-studio/pilot-cloud-infra/issues/39

- Reviewed PR: qf-studio/pilot-cloud-infra#38
- Superseded approach: qf-studio/pilot-cloud-infra#35, #36 (executor cannot work in that package — see memory `model-refusal-looks-like-exit-status-1`)
