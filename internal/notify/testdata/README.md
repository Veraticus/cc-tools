# testdata provenance

Task-launch fixtures (`tasks_*.jsonl`) and goal fixtures (`goal_*.jsonl`) are
built from real Claude Code 2.1.150/2.1.200/2.1.201 transcript records
(agent launch, background bash launch, task-notification completions, and
goal_status attachments), with session ids, uuids, agent/task ids, file
paths, and prose trimmed or replaced with placeholder text of similar shape.
Structural field paths (`message.content[].tool_use_id`, `attachment.type`,
`<task-id>` wrapper, etc.) are kept exact so the fixtures stay honest about
what real transcripts look like.

Two records could not be sourced from any real transcript on this machine
(searched every transcript under `~/.claude/projects/*/*.jsonl` containing a
`goal_status` attachment) and are synthesized by editing a real verdict line:

- `goal_failed.jsonl`: no transcript here ever recorded `"failed":true`. The
  second line is a real `met:false` verdict (halmasuit session
  `3fd16976-f607-4ca9-8de0-a758d44f0b60`, iteration verdict) with
  `"failed":true` added.
- `goal_active_iterating.jsonl`: no transcript here ever recorded an
  `iterations` field on a `met:false` (non-terminal) verdict — every real
  example of `iterations` appears only on the terminal `met:true` record,
  summarizing total loop iterations for the whole goal lifecycle. The second
  line is a real `met:false` verdict (halmasuit session
  `3fd16976-f607-4ca9-8de0-a758d44f0b60`) with `"iterations":2` added so the
  scanner's generic iteration-capture path has a fixture to exercise.
- `goal_incident_daemon.jsonl`: constructed (not sourced from a real
  transcript) to reproduce the July 5 grailquest incident — a live
  background-Bash daemon parked under an armed goal, which Claude Code's
  built-in `/goal` evaluator silently skips re-evaluating whenever a Stop
  finds a live background task, stalling the goal forever. The Bash
  `tool_use`/`tool_result` pair mirrors the real background-launch shape
  from `tasks_live.jsonl` with a daemon-shaped command substituted in place
  of the original build command; the leading `goal_status` sentinel record
  mirrors `goal_active_live_tasks.jsonl`'s shape with a condition rewritten
  to describe the daemon.
