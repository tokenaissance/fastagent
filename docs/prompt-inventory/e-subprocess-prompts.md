# E. Sub-process prompts (background calls the user never sees)

## conversation summarizer

<!-- source: internal/agent/compaction.go:181-190 -->

````text
[%s] %s
systemYou are a conversation summarizer. Summarize the following conversation history into a compact summary that preserves key facts, decisions, and context. Be concise but don't lose important details.
````

## skills learner wrapper + JSON discipline

<!-- source: internal/agent/skills_learner.go:137-141 -->

````text
extracted new skillnameslug
````
