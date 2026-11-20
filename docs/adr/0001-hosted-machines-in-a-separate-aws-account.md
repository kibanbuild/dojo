# ADR 0001: Hosted machines run in a separate AWS account, checked from outside

Status: proposed, 2026-11-20. Decided in the design round of 2026-11-20 (EC2, private
subnet, separate account); the checking rule is carried from the first technical design.

## Context

Dojo runs two kinds of hosted machine: the role exam (a three-hour work day) and a hosted lab for
laptops that cannot run labs. In both, the learner is root on a full Linux machine we pay for.
They can run anything: scans, miners, a process that waits for the checker. Exam results are the
only authoritative signal in the product, so what the learner controls must never be what scores
them.

## Decision

1. Machines live in their own AWS account (`dojo-machines`), apart from the account that holds the
   web, the API, the database and Bedrock. A compromise or an AWS abuse report stays in a
   disposable account. Only the runner (a small Go service in that account) holds IAM there.
2. Each machine: private subnet, no public IP, security group with no egress, no instance
   profile, IMDS disabled. The only way in is the runner's SSH and terminal gateway.
3. Exam checks never run inside the guest. On submit or at the deadline the runner stops the
   machine, attaches its root volume to a one-use checker instance with no network, and reads the
   disk there. The original volume is kept untouched until the result is final.
4. The API drives the runner over mTLS. The runner never reads our database or secrets.

## Consequences

- Two accounts and two sets of infrastructure code from the first milestone. The cost is setup
  time, not money.
- Checks can only see disk state, so Support L1 exam tickets must be written as file-state checks
  (configs, enabled units, permissions, rotation rules). Checks on a running system are a later
  design.
- Reversing this later (one account) is easy; splitting a shared account later is not. That is
  why it is decided now.
