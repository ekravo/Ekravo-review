# Agent loop contract

Most pull requests are opened by AI coding agents, not humans. The agent opens
a PR, waits for Ekravo's review, applies the fixes, pushes, and repeats until
Ekravo says the PR is green. This document defines the contract that makes
that loop converge.

## The machine block

Every review body (and every failure notice) starts with a hidden, versioned
HTML comment. It never renders in the GitHub UI, and it leads the body so
byte-budget truncation of the human markdown cannot cut it off:

```
<!-- ekravo-review:v1
{"schema":1,"pr":42,"repo":"owner/name","head_sha":"<full 40-char sha>",
 "status":"action_required","verdict":"changes","blocking":1,
 "counts":{"high":1,"low":1},
 "coverage":{"complete":true,"truncated":false,"sections_total":2,"sections_failed":0,
             "incremental":true,"since_sha":"…","dropped":0,"inline_posted":2},
 "findings":[{"id":"f-0123456789ab","file":"a.go","line":10,"severity":"high",
              "category":"bug","blocking":true,"confidence":0.9,
              "title":"…","detail":"…","fix":"…","carried":false}],
 "resolved_since_last_round":["f-…"]}
-->
```

Parse it by locating `<!-- ekravo-review:v1` and the next `-->`, then
JSON-decoding what is between them.

## Status: the done-condition

`status` is a closed enum. Gate on it, not on prose or emoji:

| status | meaning | agent action |
|---|---|---|
| `action_required` | blocking findings exist | fix every finding with `blocking:true`, push |
| `pass` | complete review, nothing to fix | done — merge or hand off |
| `pass_with_suggestions` | approved; notes are optional | done — **do not push commits just to address notes** |
| `incomplete` | truncated diff or failed sections | not a pass; request `@bot review` again |
| `error` | the review failed | retry |

A partial review can never report `pass`. Green on part of a diff is not green.

## Verdicts and GitHub review events

With `AUTO_APPROVE=true`:

- `approve` → APPROVE
- `suggestions` → APPROVE (non-blocking notes must not stall an agent waiting
  for approval; the body marks them non-blocking)
- `changes` → REQUEST_CHANGES

With `AUTO_APPROVE=false` every event is COMMENT; use the machine block's
`status` instead of the GitHub review state.

## Findings

- `id` is stable across rounds for the same defect on the same line
  (hash of file, line, category, title). Track it to know what is new,
  still open, and resolved.
- `blocking` reflects the repo's `block_on` policy. Only blocking findings
  gate the verdict; fixing non-blocking notes is optional and can attract
  fresh nits — don't loop on them.
- `carried:true` marks an open blocking finding from an earlier round that
  the new commits did not touch. Incremental re-reviews carry these forward,
  so an unrelated push cannot make an unfixed blocker disappear.
- `resolved_since_last_round` lists prior finding IDs this round no longer
  reports — confirmation your fixes landed.

## Convergence guarantees

- A push arriving while a review is in flight is coalesced, not dropped: the
  queue runs one more review with the latest head after the current one ends.
- Reviews are pinned to the head SHA they reviewed (`head_sha` in the block,
  `commit_id` on the GitHub review). Ignore a review whose `head_sha` is not
  your current head; a newer one is coming.
- Previous rounds' reviews are kept on the PR (only a forced re-review of the
  same head replaces its predecessor), so you can diff rounds.