# Deterministic workflow generation

Generate a validated version 1 workflow from Gherkin files without calling a model. Generated implementation and review prompts use an explicitly selected model when subsequently executed.

1. [x] Implement deterministic library generation with regression tests first.
2. [x] Add CLI, acceptance checks, and documentation.
3. [x] Independently review semantics, filesystem handling, and usability; fix findings with regressions.
4. Run repository checks, commit in isolated worktree, and create a draft PR with `includes-ai-code`.

Defaults: require output and model, resolve input globs relative to --repo, preserve source files, never overwrite output. Generation does not execute tasks or infer project-specific test commands.
