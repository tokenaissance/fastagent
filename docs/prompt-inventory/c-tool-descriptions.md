# C. Tool descriptions (the schema the model reads)

## apply_patch

<!-- source: internal/agent/tools/apply_patch.go:444 (via applyPatchDescription) -->

````text
Apply a multi-file patch in OpenAI Codex DSL format. Use this instead of chained edit_file/write_file calls when a change touches ≥2 files or ≥2 hunks — one tool call performs every edit atomically (parse + hunk matching happens for every file before any write; if any hunk fails to anchor, NO file is modified).

Format:

  *** Begin Patch
  *** Add File: path/new.go
  +line one
  +line two
  *** Update File: path/old.go
  *** Move to: path/renamed.go    (optional rename, before any hunk)
  @@
   keep_this_line
  -drop_this
  +add_this
   keep_this_too
  @@
   second_anchor
  -bye
  +hi
  *** End of File                  (optional; pin the previous hunk to file end)
  *** Delete File: path/legacy.go
  *** End Patch

Rules:
- Hunks anchor on context lines (' ' prefix) plus '-' lines that must literally match the file. Provide enough context to make the location unambiguous; matching is in-order, first match wins.
- Pure-add hunks (only '+' lines) only work with *** End of File or at the very top of a file.
- Identity files (SOUL.md, IDENTITY.md, MEMORY.md, AGENTS.md, BOOTSTRAP.md, TOOLS.md, HEARTBEAT.md, USER.md) accept Add and Update but NOT Delete or Move.
- Path resolution matches read_file/write_file: workspace-relative paths go to the workspace store, identity-file basenames go to the system store, absolute paths go to disk.
````

## apply_patch

<!-- source: internal/agent/tools/apply_patch.go:444 (via applyPatchDescription) -->

````text
Apply a multi-file patch in OpenAI Codex DSL format. Use this instead of chained edit_file/write_file calls when a change touches ≥2 files or ≥2 hunks — one tool call performs every edit atomically (parse + hunk matching happens for every file before any write; if any hunk fails to anchor, NO file is modified).

Format:

  *** Begin Patch
  *** Add File: path/new.go
  +line one
  +line two
  *** Update File: path/old.go
  *** Move to: path/renamed.go    (optional rename, before any hunk)
  @@
   keep_this_line
  -drop_this
  +add_this
   keep_this_too
  @@
   second_anchor
  -bye
  +hi
  *** End of File                  (optional; pin the previous hunk to file end)
  *** Delete File: path/legacy.go
  *** End Patch

Rules:
- Hunks anchor on context lines (' ' prefix) plus '-' lines that must literally match the file. Provide enough context to make the location unambiguous; matching is in-order, first match wins.
- Pure-add hunks (only '+' lines) only work with *** End of File or at the very top of a file.
- Identity files (SOUL.md, IDENTITY.md, MEMORY.md, AGENTS.md, BOOTSTRAP.md, TOOLS.md, HEARTBEAT.md, USER.md) accept Add and Update but NOT Delete or Move.
- Path resolution matches read_file/write_file: workspace-relative paths go to the workspace store, identity-file basenames go to the system store, absolute paths go to disk.
````

## bash_output

<!-- source: internal/agent/tools/bash_tools.go:25 (via bashOutputDescription) -->

````text
Read new stdout/stderr from a backgrounded shell since the last call. Use this to monitor a long-running process started with exec(run_in_background=true).

Returns:
  - new output produced since the previous bash_output call on this bash_id (each call advances a per-session cursor)
  - "[status] running" or "[status] exited (code=N)" — only "exited" rows guarantee the process is done; killed processes report code=-1 with the kill reason appended
  - a "[truncated]" line prepended if the 4 MiB per-session output buffer rolled past the read cursor (oldest bytes dropped FIFO)

Notes:
  - The session keeps running across calls until kill_shell or natural exit.
  - After exit, bash_output is still callable to read any final output and confirm the exit code.
  - The optional 'filter' regex is applied per output line (lines that don't match are dropped before return) — useful for tailing a noisy log when you only care about errors.
````

## kill_shell

<!-- source: internal/agent/tools/bash_tools.go:37 (via killShellDescription) -->

````text
Terminate a backgrounded shell started by exec(run_in_background=true). Sends SIGKILL via process-group cancellation. Idempotent — calling it on an already-exited shell is a no-op and returns success.
````

## get_billing_usage

<!-- source: internal/agent/tools/billing.go:13 -->

````text
Get the current user's token usage and remaining quota for billing. Use this when the user asks how many tokens they used, how much quota remains, whether they are over limit, when quota resets, or what plan allowance they have. This is read-only and only returns the current billing account; it cannot inspect other users.
````

## create_cron_job

<!-- source: internal/agent/tools/cron.go:29 -->

````text
Create a scheduled task. Use this for any user request that names a specific time, an interval, or a recurring schedule (e.g. "5 分钟后提醒", "every Monday 9am", "each day at 8"). When the schedule fires, the agent receives `message` as a fresh inbound prompt on the same channel the request originated from. Do NOT write timed reminders into HEARTBEAT.md — that file is only for conditional self-checks reviewed at every heartbeat tick.
````

## list_cron_jobs

<!-- source: internal/agent/tools/cron.go:60 -->

````text
List all scheduled tasks for this agent.
````

## delete_cron_job

<!-- source: internal/agent/tools/cron.go:72 -->

````text
Delete a scheduled task by ID.
````

## delegate_task

<!-- source: internal/agent/tools/delegate.go:28 -->

````text
Spawn a sub-agent with its OWN context and OWN iteration budget to run a single bounded sub-task. Use this when the user's request decomposes into several large independent chunks (e.g. "find 10 leads matching X" then "find another 10 matching Y" then "write 5 emails from this data"). Each sub-agent gets a fresh tool-iteration budget so you don't burn yours exploring, and your own context stays clean of the dozens of intermediate tool results the sub-agent goes through. 

**Sub-agents run SERIALLY, not in parallel.** Even if you emit 5 delegate_task calls in one round, they execute one at a time — they share the single sandbox + single browser daemon, so parallel execution would trample each other's state. Expect the wall-clock time of a fan-out to be N × the single-sub-agent time, not 1× it. Plan accordingly: smaller per-sub-agent scope is better than fewer, larger calls.

The sub-agent runs against the same tools and provider you have (minus delegate_task itself — no nesting). It cannot see your prior conversation, so pass everything it needs in the `task` arg: criteria, search hints, earlier findings to build on, output format. Sub-agents are best for tasks that produce a self-contained artifact (a table, a draft email, a structured summary). 

Return: the sub-agent's final text exactly as it produced it. You then assemble multiple sub-agent results into the final deliverable for the user.
````

## exec

<!-- source: internal/agent/tools/exec.go:170 -->

````text
Execute a shell command and return stdout/stderr. For binary or image output (PNG, JPEG, PDF, audio, video), write the file into the workspace (e.g. ./out.png) and reference it by relative path in your reply — do NOT base64-encode it into stdout, and do NOT inline data: URLs in your response. The workspace file will be surfaced to the user via the Files panel.
````

## exec

<!-- source: internal/agent/tools/exec.go:513 -->

````text
Execute a shell command in the sandbox and return stdout/stderr. For binary or image output (PNG, JPEG, PDF, audio, video), write the file into the workspace (e.g. ./out.png) and reference it by relative path in your reply — do NOT base64-encode it into stdout, and do NOT inline data: URLs in your response. The workspace file will be surfaced to the user via the Files panel.
````

## read_file

<!-- source: internal/agent/tools/file.go:348 -->

````text
Read the contents of a file
````

## write_file

<!-- source: internal/agent/tools/file.go:357 -->

````text
Write content to a file (creates directories as needed)
````

## list_dir

<!-- source: internal/agent/tools/file.go:373 -->

````text
List files and directories in a path
````

## edit_file

<!-- source: internal/agent/tools/file.go:63 (via editDescription) -->

````text
Edit a file by replacing an exact substring. Prefer this over write_file when changing only part of a file (especially identity files like SOUL.md / MEMORY.md): it's cheaper, can't drop unrelated content, and validates the replacement was applied. old_string must match a unique substring unless replace_all is true; new_string must differ from old_string. Read the file first if you're unsure of the exact text.
````

## read_file

<!-- source: internal/agent/tools/file.go:906 -->

````text
Read the contents of a file
````

## write_file

<!-- source: internal/agent/tools/file.go:1000 -->

````text
Write content to a file (creates directories as needed)
````

## list_dir

<!-- source: internal/agent/tools/file.go:1073 -->

````text
List files and directories in a path
````

## edit_file

<!-- source: internal/agent/tools/file.go:63 (via editDescription) -->

````text
Edit a file by replacing an exact substring. Prefer this over write_file when changing only part of a file (especially identity files like SOUL.md / MEMORY.md): it's cheaper, can't drop unrelated content, and validates the replacement was applied. old_string must match a unique substring unless replace_all is true; new_string must differ from old_string. Read the file first if you're unsure of the exact text.
````

## update_goal

<!-- source: internal/agent/tools/goal.go:17 -->

````text
Mark the active goal complete. Status is restricted to "complete"; pausing, resuming, and budget_limited transitions are controlled by the user or the runtime, not by the model. Only call this when the objective has actually been achieved and no required work remains — do not call it merely because the budget is nearly exhausted or because you want to stop.
````

## image_gen

<!-- source: internal/agent/tools/image_gen.go:24 -->

````text
Generate images from a text prompt. Uses a configurable provider chain (OpenAI gpt-image-1, fal flux, …) with automatic fallback. Returns markdown image tags that render inline in chat.
````

## knowledge_search

<!-- source: internal/agent/tools/knowledge_search.go:26 -->

````text
Search the agent's knowledge base (reference files uploaded by the agent owner) by keyword. Use focused keywords from the question; if a search misses, retry once or twice with different or broader terms.
````

## load_skill

<!-- source: internal/agent/tools/load_skill.go:18 -->

````text
Load the full content of a skill by name. Use this when you need detailed instructions for a specific skill.
````

## memory_search

<!-- source: internal/agent/tools/memory_search.go:38 -->

````text
Search through conversation history logs using keyword matching with recency weighting
````

## message

<!-- source: internal/agent/tools/message.go:31 -->

````text
Send a message to a channel
````

## set_preference

<!-- source: internal/agent/tools/preference.go:21 -->

````text
Save a personal preference or API key for the current chatter on this agent. Use this when the user wants to configure something that should persist across conversations — for example their timezone, language, an API key for image generation, drawing style, etc. The preference is scoped to this user + this agent only, not shared with other agents or users.
````

## search_skills

<!-- source: internal/agent/tools/skill_install.go:22 -->

````text
Search for skills on skills.sh (primary registry) and clawhub.ai. Returns the top matches so you can pick one to install.
````

## install_skill

<!-- source: internal/agent/tools/skill_install.go:56 -->

````text
Install a skill into THIS agent's private skills directory. Tries skills.sh first, then clawhub.ai. If neither has it, returns a not-found error — at that point ask the user whether to build a custom skill with the skill-creator skill instead of retrying. Installed skills are scoped to this agent only; they do not affect other agents.
````

## spawn_subagent

<!-- source: internal/agent/tools/subagent.go:23 -->

````text
Spawn another agent as a sub-task and return its response. Use this to delegate work to specialized agents.
````

## set_timezone

<!-- source: internal/agent/tools/timezone.go:26 -->

````text
Record the current chatter's timezone. Call this whenever the chatter tells you their timezone, city, or country (e.g. "我在北京" → Asia/Shanghai). This persists the timezone to the chatter's profile so future sessions use their local time automatically.
````

## tts

<!-- source: internal/agent/tools/tts.go:22 -->

````text
Convert text to speech. Uses a configurable provider chain (OpenAI tts-1, MiniMax speech-02, …) with automatic fallback. The audio file is attached to the chat message automatically.
````

## web_fetch

<!-- source: internal/agent/tools/web_fetch.go:109 (via webFetchDescription) -->

````text
Fetch a single known URL and return its plain text. Use this only after you already know the exact target page URL. If the user's message itself contains a URL or bare domain (e.g. 'idoubi.ai', 'https://example.com/cv'), fetch THAT URL directly — prepend https:// for bare domains — instead of running web_search first. For search intent like 'search/find/look up', 'nearby', 'events', 'news', 'reviews', 'weather', 'latest', or any request where you do not already have a concrete page URL, call web_search first. Never web_fetch search result pages such as google.com/search, bing.com/search, baidu.com/s, or duckduckgo.com/?q=; use web_search for those queries instead. DO NOT guess URLs from memory: your training data has stale paths and you will burn rounds on 404s. When the user described a page in natural language with no URL, run web_search first to discover the URL, then web_fetch that exact URL. If web_fetch on a concrete page fails with 401/403/429, captcha, anti-bot, or JavaScript-required output, use the camoufox-cli skill in the sandbox against the same URL instead of retrying web_fetch. If web_search isn't available, prefer well-known stable hosts (en.wikipedia.org, github.com), not date-stamped article URLs. A URL that returned 4xx/5xx earlier in this turn will be refused if you retry it.
````

## web_fetch

<!-- source: internal/agent/tools/web_fetch.go:109 (via webFetchDescription) -->

````text
Fetch a single known URL and return its plain text. Use this only after you already know the exact target page URL. If the user's message itself contains a URL or bare domain (e.g. 'idoubi.ai', 'https://example.com/cv'), fetch THAT URL directly — prepend https:// for bare domains — instead of running web_search first. For search intent like 'search/find/look up', 'nearby', 'events', 'news', 'reviews', 'weather', 'latest', or any request where you do not already have a concrete page URL, call web_search first. Never web_fetch search result pages such as google.com/search, bing.com/search, baidu.com/s, or duckduckgo.com/?q=; use web_search for those queries instead. DO NOT guess URLs from memory: your training data has stale paths and you will burn rounds on 404s. When the user described a page in natural language with no URL, run web_search first to discover the URL, then web_fetch that exact URL. If web_fetch on a concrete page fails with 401/403/429, captcha, anti-bot, or JavaScript-required output, use the camoufox-cli skill in the sandbox against the same URL instead of retrying web_fetch. If web_search isn't available, prefer well-known stable hosts (en.wikipedia.org, github.com), not date-stamped article URLs. A URL that returned 4xx/5xx earlier in this turn will be refused if you retry it.
````

## web_search

<!-- source: internal/agent/tools/web_search.go:26 -->

````text
Search the web and return results with titles, URLs, and snippets. Backed by a configurable provider chain (e.g. exa, brave, searxng) with automatic fallback.
````
