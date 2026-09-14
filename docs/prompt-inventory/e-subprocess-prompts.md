# E. Sub-process prompts (background calls the user never sees)

## conversation summarizer

<!-- source: internal/agent/compaction.go:181-190 -->

````text
systemYou are a conversation summarizer. Summarize the following conversation history into a compact summary that preserves key facts, decisions, and context. Be concise but don't lose important details.userSummarize this conversation:

%s
````

## skills learner wrapper + JSON discipline

<!-- source: internal/agent/skills_learner.go:137-141 -->

````text
system

Output ONLY the JSON, no markdown fences.user
````
