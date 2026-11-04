# Bundled Compose schema

Source: https://github.com/compose-spec/compose-spec/blob/main/schema/compose-spec.json
Retrieved: 2026-10-21. The accompanying compose-spec.LICENSE applies.

This schema supplies offline completion and validation in the editor. Vigara’s server still validates its supported deployment features when saving.

Monaco is pinned to 0.52.2 because monaco-worker-manager’s initialization protocol does not work with Monaco 0.53 and later (https://github.com/microsoft/monaco-editor/issues/5310). Verify YAML worker validation/completion in a real browser before upgrading it.
