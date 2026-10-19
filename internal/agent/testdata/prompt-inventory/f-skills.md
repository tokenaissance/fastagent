# F. Skills (the source files are the prompt)

Skill text is not a Go string: it is `SKILL.md` inside each skill directory, loaded on
demand by `load_skill` and summarised into the system prompt by the `skills` module.
Reviewing a skill means reviewing its file; nothing is duplicated here on purpose.

## repo `skills/` (shipped with the binary)

- `skills/code-runner/`
- `skills/data-analysis/`
- `skills/fastagent-api-integration/`
- `skills/fastagent-skill-guide/`
- `skills/fastagent-skill-learner/`
- `skills/find-skills/`
- `skills/image-gen/`
- `skills/skill-creator/`
- `skills/translation/`
- `skills/web-search/`

## embedded `internal/agent/bundled_skills/` (injected into every agent home)

- `internal/agent/bundled_skills/README.md`
- `internal/agent/bundled_skills/camoufox-cli`
- `internal/agent/bundled_skills/find-skills`
- `internal/agent/bundled_skills/skill-creator`

## the skills-learner prompt

`internal/agent/skills_learner.go` reads its instruction from the
`fastagent-skill-learner` skill (`loadSkillLearnerPrompt`), so that file is the prompt —
review `skills/fastagent-skill-learner/SKILL.md`.
