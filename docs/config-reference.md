# config.json Reference

The complete reference for Late's `config.json`: every accepted key, its type,
its default, the CLI flag it mirrors where one exists, and what it does. The
tables are kept in lockstep with the `Config` struct in
`internal/config/config.go` by a reflection test
(`internal/config/docs_test.go`).

For a guided setup read the [Quickstart](quickstart.md) first; this page is the
exhaustive reference.

## File location

| Platform | Path |
| --- | --- |
| macOS | `~/Library/Application Support/late/config.json` |
| Linux | `~/.config/late/config.json` (honors `XDG_CONFIG_HOME`) |
| Windows | `%APPDATA%\late\config.json` |

A missing file is not an error: on first run Late writes a default config that
enables every built-in tool. The file and its directory are permission-hardened
to `0600` / `0700` on every load. Two naming conventions coexist: the compaction
entries use kebab-case, while the older provider/tool entries use snake_case —
both spellings are exactly as listed below.

## How values are parsed and validated

Late decodes `config.json` with `encoding/json`. What that means in practice:

* **Wrong-typed values are fatal** — a string where a number is required aborts
  startup with the decoder's error, and no TUI is launched.
* **Unknown top-level keys are currently ignored** — a typo at the top level
  silently does nothing. The one exception is the next rule, which covers the
  section where hand-edited typos hurt most.
* **Unknown keys inside `models[]` entries are fatal and located** — every
  entry's keys are validated against the known set (`id`, `url`, `key`,
  `model`, `jev-autocompact-percent`), reporting an unknown key with its
  1-based line and column, the entry's name (its `id`, else its `model`), and
  a did-you-mean suggestion when a known key is within edit distance 3:

  ```
  error in /Users/u/Library/Application Support/late/config.json at line 6, column 7: models[local] entry "urll" is not a valid entry key. Did you mean "url"?
  ```

  ```
  error in /Users/u/Library/Application Support/late/config.json at line 12, column 7: models[frontier] entry "jev-autocompact_percent" is not a valid entry key. Did you mean "jev-autocompact-percent"?
  ```

* **Value-range problems only warn** — a value that parses but is out of range
  (e.g. `compaction-threshold-percent: 400`, a `jev-autocompact-percent` of
  `0.5`, an invalid `compaction-mode`) prints one `Warning:` line at startup
  and falls back to that setting's default; it never aborts startup.

Boolean entries are plain JSON `true`/`false` — no other spellings are
accepted.

## Examples

### Minimal starter

```json
{
  "models": [
    {
      "id": "local",
      "url": "http://localhost:8080",
      "key": "",
      "model": "qwen3.6-35b-a3b"
    }
  ]
}
```

### Fuller example

The compaction block (score cutoff, context percentages, gate knobs, offline
backend, auto-compaction, retrieval) plus the provider block. Note that
`"compaction-backend": "offline"` selects the deterministic scripted scorer for
demos and tests — no API key, no network — and must never become a production
default. The `frontier` model entry also carries a per-model
`jev-autocompact-percent` override: the agents routed to it auto-compact at
70% of their window while every other agent uses the global 99%.

```json
{
  "enabled_tools": {
    "read_file": true,
    "write_file": true,
    "target_edit": true,
    "bash": true
  },
  "permission-mode": "ask-for-user-approval",
  "models": [
    {
      "id": "frontier",
      "url": "https://api.deepseek.com",
      "key": "sk-your-key",
      "model": "deepseek-flash",
      "jev-autocompact-percent": 70
    },
    {
      "id": "local",
      "url": "http://localhost:8080",
      "key": "",
      "model": "qwen3.6-35b-a3b"
    }
  ],
  "agent_models": {
    "orchestrator": "frontier",
    "coder": "local"
  },

  "compaction-mode": "enabled",
  "compaction-threshold": 0.65,
  "compaction-threshold-percent": 80,
  "compaction-max-elide-percent": 70,
  "compaction-protected-floor": 5,
  "compaction-backend": "offline",
  "jev-autocompact": true,
  "jev-autocompact-percent": 99,
  "compaction-retrieval": true
}
```

## Key reference

Types: `string`, `number`, `boolean`, `array`, `object`. "CLI flag" is the
equivalent flag where one exists; Go's flag package accepts one or two dashes
(`-flag` / `--flag`).

### Models and providers

| Key | Type | Default | CLI flag | Description |
| --- | --- | --- | --- | --- |
| `models` | array | `[]` | — | Model registry for `/model` and `agent_models`; each entry is `{id, url, key, model, jev-autocompact-percent}` (see below). |
| `agent_models` | object | `{}` | — | Maps agent roles (`orchestrator`, `researcher`, `coder`, …) to a `models` entry `id` (or, legacy, its model name); persisted by `/model`. |
| `openai_base_url` | string | `http://localhost:8080` | — | Base URL of the main OpenAI-compatible API; `OPENAI_BASE_URL` env overrides when set. |
| `openai_api_key` | string | `""` | — | API key for the main provider; `OPENAI_API_KEY` env overrides when set. |
| `openai_model` | string | `""` | — | Main model id used when no `models`/`agent_models` routing applies; `OPENAI_MODEL` env overrides when set. |
| `late_subagent_base_url` | string | `""` (inherits main) | — | Dedicated subagent base URL; wins over the legacy `subagent_base_url`; `LATE_SUBAGENT_BASE_URL` env overrides when set. |
| `late_subagent_api_key` | string | `""` (inherits main) | — | Dedicated subagent API key; wins over the legacy `subagent_api_key`; `LATE_SUBAGENT_API_KEY` env overrides when set. |
| `late_subagent_model` | string | `""` (inherits main) | — | Dedicated subagent model; wins over the legacy `subagent_model`; `LATE_SUBAGENT_MODEL` env overrides when set. |

### Supervision and preferences

| Key | Type | Default | CLI flag | Description |
| --- | --- | --- | --- | --- |
| `permission-mode` | string | `"ask-for-user-approval"` | `--ask-for-user-approval` / `--unsupervised` | How dangerous commands are supervised: `"ask-for-user-approval"` or `"unsupervised"`; a passed flag overrides config and the flags are mutually exclusive; an invalid value warns and falls back to the safe default. |
| `save_subagent_histories` | boolean | `false` | `--save-subagent-histories` | Persist subagent conversation histories under `<sessions>/<session-id>/subagents/` (precedence: explicitly passed flag > per-session saved preference > this entry). |
| `theme` | string | `""` | `--theme` | Plugin theme id (`plugin:name` or bare name); precedence `--theme` > `LATE_THEME` env > this entry > bundled base; persisted by `/themes`. |
| `skills_dir` | string | `""` | — | Legacy entry, currently unused: skill discovery reads the platform skills directory (`~/.config/late/skills/` etc.) and project-local `.late/skills/`, not this value. |

### Tools and output

| Key | Type | Default | CLI flag | Description |
| --- | --- | --- | --- | --- |
| `enabled_tools` | object | all built-in tools `true` | — | Per-tool switches (see below); missing entries are filled from the defaults. |

### Context compaction

`compaction-threshold`, `compaction-threshold-percent`, and
`jev-autocompact-percent` are three different knobs: a per-segment SCORE
cutoff, the context-usage level the info bar reports headroom for, and the
context-usage level that fires the auto-trigger.

| Key | Type | Default | CLI flag | Description |
| --- | --- | --- | --- | --- |
| `compaction-mode` | string | `"shadow"` | `--compaction-mode` | Staged rollout stage: `off` (no scoring), `shadow` (score + shadow log only, no behavior change), or `enabled` (also relocate low-scoring segments and register the `expand` tool); invalid values warn and fall back to `shadow`. |
| `compaction-threshold` | number | `0.35` | `--compaction-threshold` | Elision score cutoff: segments scoring strictly below it are elided when `compaction-mode` is `enabled`; valid range (0,1]; out-of-range values warn and fall back. |
| `compaction-threshold-percent` | number | `80` | — | Context-usage percentage the TUI info bar reports remaining headroom against; 1-100 valid, `0` = default, anything else warns and falls back. |
| `compaction-max-elide-percent` | number | `70` | — | Elide-fraction tripwire: when the scorer wants to elide more than this share of an output's tokens it is distrusted and NOTHING is elided; 1-100 valid; cannot be fully disabled. |
| `compaction-protected-floor` | number | `5` | — | Score floor (as a percentage) under which protected segment kinds (stacktrace, diff) may be elided; at any higher score they are kept; 1-100 valid. |
| `compaction-backend` | string | `""` | — | Where scores come from; only `"offline"` today (deterministic scripted scorer, demos/tests only); a set value wins over `JEV_API`/auto-detection; an invalid value warns and falls back to env resolution. |
| `compaction-retrieval` | boolean | `false` | — | Read side of the record store: before every request the top-k relevant digest summaries are appended to the request's work area; inert (warns) unless `compaction-mode` is `enabled`. |
| `jev-autocompact` | boolean | `false` | — | Run the full-history compaction (`/jev-compact-context` flow) automatically when context usage crosses `jev-autocompact-percent`. |
| `jev-autocompact-percent` | number | `99` | — | Context-usage percentage that fires the auto-trigger; 1-100 valid, `0` = default, anything else warns and falls back. Each `models[]` entry can override it for the agents routed to that model (see the `models` entries schema below). |

### Legacy entries

| Key | Type | Default | CLI flag | Description |
| --- | --- | --- | --- | --- |
| `subagent_base_url` | string | `""` | — | Legacy subagent base URL; superseded by `late_subagent_base_url`, which wins when both are set. |
| `subagent_api_key` | string | `""` | — | Legacy subagent API key; superseded by `late_subagent_api_key`. |
| `subagent_model` | string | `""` | — | Legacy subagent model; superseded by `late_subagent_model`. |

## Nested schemas

### `models` entries

Each element of the `models` array is an object:

* `id` (string, optional) — stable identifier referenced by `agent_models` and
  the `/model` picker; omit it to fall back to the model name.
* `url` (string, required) — OpenAI-compatible base URL.
* `key` (string, required, may be `""`) — API key; local servers need none.
* `model` (string, required) — the model name the provider serves.
* `jev-autocompact-percent` (number 1-100, optional, default = the global
  `jev-autocompact-percent`) — per-model override of the auto-compaction
  trigger: different models have different context sizes, so the percentage
  at which compaction should fire is a property of the model, not just of the
  installation. Resolution for any agent: its `agent_models`-routed model
  entry's value (when valid) > the global `jev-autocompact-percent` > `99`.
  Out-of-range values warn at startup and fall back to the global (the
  models[] key walk covers key names, not value ranges).

Unknown keys inside an entry are a fatal located error (see
[How values are parsed and validated](#how-values-are-parsed-and-validated)) —
the same rule the decoder cannot apply at the top level, enforced where the
typed decode would silently drop a hand-edited typo.

### `agent_models` values

Keys are agent roles (`orchestrator`, `researcher`, `coder`); values reference
a `models` entry by its `id` (preferred — providers exposing the same model
name stay distinguishable) or, for configs created before ids existed, by the
model name.

### `enabled_tools` entries

Keys are tool names, values booleans. Defaults (all `true`): `read_file`,
`write_file`, `target_edit`, `spawn_subagent`, `bash`, `search_content`,
`find_files`, `create_todos`, `list_todos`, `finish_todo`. Missing entries are
merged from the defaults on load. When `compaction-mode` is `enabled`, the
`expand` tool is additionally registered so elided originals stay retrievable.
