# RoomPlay — Protected Git Workflow

This document applies to **all teammates and AI coding agents**. `main` is an integration branch protected by GitHub rulesets, not a place for direct commits. Follow `AGENTS.md` and your `docs/agents/<NAME>.md` guide.

## Start each task
```sh
git status --short
git switch main
git pull --ff-only origin main
git switch -c feat/short-descriptive-name
git branch --show-current     # MUST NOT be main
```

Choose a unique short-lived branch relevant to the task, such as `feat/coordinator-registry`, `feat/player-state-machine`, `feat/web-room-list`, `feat/library-catalog`, `test/multiroom-integration`, or `docs/...`. If a task branch already exists, switch to it instead of recreating it.

## Commit and push ONLY a feature branch
```sh
git branch --show-current     # verify the target before every write
git status --short
git add <only-files-you-own>
git commit -m "feat: implement scoped change"
git push -u origin feat/short-descriptive-name
```
Replace the sample branch name with the **actual current feature branch**. Never use `git push origin main`, `git push --force origin main`, or `git commit` while checked out on main.

## Pull request
Create a GitHub PR **head = your task branch, base = main**, link your issue, describe tests and contract impacts, and request review. Pass CI (format, unit tests, race, vet, build) and satisfy the branch-protection ruleset. No self-approval. João reviews teammate code; a different teammate reviews João's PRs. Changes to protocol/API require João's architectural sign-off.

After merge, update local main and start a *new* feature branch for the next task. Delete merged branches when safe.

## AI agent preflight checklist
Before performing ANY Git write or PR action, the agent must:
1. Identify the current branch (`git branch --show-current` or equivalent API ref).
2. Verify the destination is NOT `main` for file changes/commits/pushes.
3. Verify the PR base is `main` and PR head is a distinct task branch.
4. Confirm the files are within the teammate's ownership.
5. Stop on ruleset/review/CI rejection rather than disable or bypass restrictions.

**Forbidden even if technically permitted by GitHub:** direct file edits on `main` through GitHub APIs, branch deletion or force push of `main`, bypassing rulesets, unauthorized merges.

## Important limitations
Written agent rules help prevent accidental direct writes but **cannot substitute for enforced GitHub permissions/rulesets**. The repository owner must keep a ruleset targeting `main`, require PR reviews and CI checks, and ensure bypass permissions are tightly controlled. Contributors may push to their own feature branches.
