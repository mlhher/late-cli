# config.json Reference

The reference for Late's `config.json`: every accepted key, its type, its
default, the CLI flag it mirrors, and what it does. Every key below is kept
honest against the `Config` and `ModelSetting` structs in
`internal/config/config.go` by a test (`internal/config/docs_test.go`).

For a guided setup read the [Quickstart](quickstart.md) first; this page is the
key reference.

## File location

| Platform | Path |
| --- | --- |
| macOS | `~/Library/Application Support/late/config.json` |
| Linux | `~/.config/late/config.json` (honors `XDG_CONFIG_HOME`) |
| Windows | `%APPDATA%\late\config.json` |

A missing file is not an error: on first run Late writes a default config that
enables every built-in tool. The file and its directory are permission-hardened
to `0600` / `0700` on every load. Config entries use snake_case for the
provider/tool names and kebab-case for the newer per-model and supervision
entries — both spellings are exactly as listed below.

## Precedence

For every CLI-equivalent entry the resolution is:

> **explicitly passed flag > config.json > built-in default**

A flag that was not passed never wins: Late records which flags were
explicitly passed (`flag.Visit`) and defers to the config entry otherwise.

Layered exceptions to that order:

* Provider settings — the environment overrides config when set:
  `OPENAI_BASE_URL`, `OPENAI_API_KEY`, `OPENAI_MODEL`,
  `LATE_SUBAGENT_BASE_URL`, `LATE_SUBAGENT_API_KEY`, `LATE_SUBAGENT_MODEL`.
  Within config, `late_subagent_*` wins over the legacy `subagent_*` entries,
  which win over the main `openai_*` values.
* `theme` — `--theme` flag > `LATE_THEME` env > config > bundled base theme.
* The three `permission-mode` flags are mutually exclusive: pass at most one.
* Booleans are plain JSON booleans here: `true`, `false`, or absent.

## Strict parsing

Late prefers a located error over silently accepting a typo'd config:

* **Syntax errors** abort startup with the decoder's message.
* **Unknown config entries** are ignored at load (the decoder skips them), so
  a typo does not crash the run — but it also takes no effect, which is why
  this reference exists.
* **Permission problems** on the config file or its directory are surfaced at
  startup.

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

```json
{
  "enabled_tools": {
    "read_file": true,
    "write_file": true,
    "target_edit": true,
    "bash": true
  },
  "models": [
    {
      "id": "frontier",
      "url": "https://api.deepseek.com",
      "key": "sk-your-key",
      "model": "deepseek-flash"
    },
    {
      "id": "local",
      "url": "http://localhost:8080",
      "key": "",
      "model": "qwen3.6-35b-a3b",
      "context-size-tokens": 32768
    }
  ],
  "agent_models": {
    "orchestrator": "frontier",
    "coder": "local"
  },
  "save_subagent_histories": true
}
```

## Key reference

### Models and providers

| Key | Type | Default | CLI flag | Description |
| --- | --- | --- | --- | --- |
| `models` | array | `[]` | — | Model registry for `/model` and `agent_models`; each entry is `{id, url, key, model, context-size-tokens}` (see the nested schema below). |
| `agent_models` | object | `{}` | — | Maps agent roles (`orchestrator`, `researcher`, `coder`, …) to a `models` entry `id` (or, legacy, its model name); persisted by `/model`. |
| `openai_base_url` | string | `http://localhost:8080` | — | Base URL of the main OpenAI-compatible API; `OPENAI_BASE_URL` env overrides when set. |
| `openai_api_key` | string | `""` | — | API key for the main provider; `OPENAI_API_KEY` env overrides when set. |
| `openai_model` | string | `""` | — | Main model id used when no `models`/`agent_models` routing applies; `OPENAI_MODEL` env overrides when set. |
| `context-size-tokens` | number | `0` | — | Single-model fallback for the per-entry `models[].context-size-tokens`: declares the model's context window for setups without `agent_models` routing (the orchestrator and the subagent share it). `0`/unset = unknown (auto-discovery only). |
| `late_subagent_base_url` | string | `""` (inherits main) | — | Dedicated subagent base URL; wins over the legacy `subagent_base_url`; `LATE_SUBAGENT_BASE_URL` env overrides when set. |
| `late_subagent_api_key` | string | `""` (inherits main) | — | Dedicated subagent API key; wins over the legacy `subagent_api_key`; `LATE_SUBAGENT_API_KEY` env overrides when set. |
| `late_subagent_model` | string | `""` (inherits main) | — | Dedicated subagent model; wins over the legacy `subagent_model`; `LATE_SUBAGENT_MODEL` env overrides when set. |

### Subagents

| Key | Type | Default | CLI flag | Description |
| --- | --- | --- | --- | --- |
| `save_subagent_histories` | boolean | `false` | `--save-subagent-histories` | Persist subagent conversation histories under `<sessions>/<session-id>/subagents/`. |

### Supervision and retries

| Key | Type | Default | CLI flag | Description |
| --- | --- | --- | --- | --- |
| `permission-mode` | string | `"ask-for-user-approval"` | `--ask-for-user-approval` / `--i-promise-i-have-backups-and-will-not-file-issues` / `--force-revaluate-dangerous-commands` | How dangerous commands are supervised: one of the three mode values, each also a CLI flag; the flags override config and are mutually exclusive. |

### Tools and output

| Key | Type | Default | CLI flag | Description |
| --- | --- | --- | --- | --- |
| `enabled_tools` | object | all built-in tools `true` | — | Per-tool switches (see the nested schema below); missing entries are filled from the defaults. |
| `skills_dir` | string | `""` | — | Legacy entry, currently unused: skill discovery reads the platform skills directory (`~/.config/late/skills/` etc.) and project-local `.late/skills/`, not this value. |
| `theme` | string | `""` | `--theme` | Plugin theme id (`plugin:name` or bare name); precedence `--theme` > `LATE_THEME` env > this entry > bundled base; persisted by `/themes`. |

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
* `context-size-tokens` (number, optional, default `0` = unknown) — declares
  the model's context window in tokens for backends that never advertise it.
  llama.cpp is auto-discovered (the `/props` and `/v1/models` probes read
  `n_ctx`); generic OpenAI-compatible providers and truncating local gateways
  return nothing, which leaves the context size unknown and the predictive
  compaction heuristic dark. Set this to the model's real window (e.g.
  `32768`) and late behaves as if the backend had advertised it: the
  declared value OVERRIDES discovery (a probe result never clobbers it) and
  also back-fills when discovery finds nothing. Values `<= 0` are ignored.

### `agent_models` values

Keys are agent roles (`orchestrator`, `researcher`, `coder`); values reference
a `models` entry by its `id` (preferred — providers exposing the same model
name stay distinguishable) or, for configs created before ids existed, by the
model name.

### `enabled_tools` entries

Keys are tool names, values booleans. Defaults (all `true`): `read_file`,
`write_file`, `target_edit`, `spawn_subagent`, `bash`, `search_content`,
`find_files`, `create_todos`, `list_todos`, `finish_todo`. Missing entries are
merged from the defaults on load.

## Internal fields

The `Config` struct also carries fields with the `json:"-"` tag (e.g.
runtime state): they are never serialized and are **not** config.json keys —
using them as one is a no-op here, and a fatal unknown-key error under the
fork's strict parser.
