Update relevant Markdown context files, including CLAUDE.md if useful.

The goal is to help a future Claude Code session start quickly without scanning the whole project again.

Record only durable, project-specific context that helps with orientation or implementation decisions.

Prioritize:

* project purpose and current MVP scope
* important entry points and where main logic lives
* key packages/modules and what each is responsible for
* important commands for build, test, run, lint, or deploy
* architecture/design decisions already made
* coding conventions or constraints
* non-obvious implementation details
* known gotchas or traps
* current next steps or TODOs

Do not record:

* temporary chat history, or a log of what changed this session
* every file changed
* obvious facts visible from filenames alone
* vague summaries
* stale implementation details
* anything a future session can recover by reading the one or two files the note is about

Before writing any line, apply these tests — they matter more than the category lists above:

* **Pointer, not restatement.** If a fact is already explained by a comment, a test, or the code at its site, record at most a one-line pointer to that site (path + symbol/test name). Never copy the reasoning into Markdown: a duplicated explanation silently drifts out of sync with the code it describes. A gotcha entry is *one line* — the trap plus where the real detail lives, not the mechanics.
* **Earns its place.** A note is worth keeping only if it (a) changes a future decision and (b) cannot be found by opening the file it is about. If either is false, leave it out. When unsure, omit.
* **Deleting counts.** Tightening or removing a bloated/stale note is as valuable as adding one — actively prune when you touch a section.

Keep it concise. Prefer editing an existing note over adding one. Do not write a session log.
