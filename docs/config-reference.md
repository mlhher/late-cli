# config.json Reference

The complete reference for Late's `config.json`: every accepted key, its type,
its default, and what it does. This file is parsed **strictly** — an unknown
key, a wrong-typed value, an invalid enum value, or a syntax error aborts
startup with a located error (see [Strict parsing](#strict-parsing)) — so every
key below is validated against the `Config` struct in
`internal/config/config.go` by a test (`internal/config/docs_test.go`).

## File location

| Platform | Path |
| --- | --- |
| macOS | `~/Library/Application Support/late/config.json` |
| Linux | `~/.config/late/config.json` (honors `XDG_CONFIG_HOME`) |
| Windows | `%APPDATA%\late\config.json` |

A missing file is not an error: on first run Late writes a default config that
enables every built-in tool. The file and its directory are permission-hardened
to `0600` / `0700` on every load.

## Precedence

Layered exceptions to "config value wins over the built-in default":

* Provider settings — the environment overrides config when set:
  `OPENAI_BASE_URL`, `OPENAI_API_KEY`, `OPENAI_MODEL`,
  `LATE_SUBAGENT_BASE_URL`, `LATE_SUBAGENT_API_KEY`, `LATE_SUBAGENT_MODEL`.
  Within config, `late_subagent_*` wins over the legacy `subagent_*` entries,
  which win over the main `openai_*` values.
* `theme` — `--theme` flag > `LATE_THEME` env > config > bundled base theme.
* `save_subagent_histories` — flag > per-session saved preference > config.
* The two `permission-mode` flags are mutually exclusive: pass at most one.

## Boolean values (on/off synonyms)

The `save_subagent_histories` entry is a `boolean-with-synonyms` (`FlexBool`):
in addition to the JSON literals it accepts everyday synonyms, case-insensitively
and with surrounding whitespace tolerated (`"  On  "` is `true`). The JSON
numbers `1` and `0` are accepted too.

| Meaning | Accepted values |
| --- | --- |
| true side | `true`, `on`, `enabled`, `enable`, `active`, `activated`, `yes`, `y`, `1` |
| false side | `false`, `off`, `disabled`, `disable`, `no`, `not`, `n`, `0` |

Anything else is a fatal strict-parsing error. Saved configs always round-trip
to the plain `true`/`false` literals.

## Strict parsing

config.json is user-authored by hand, so every problem is fatal and located.
Late never falls back to defaults and starts anyway: the process prints one
line and exits instead of launching the TUI.

* **Syntax errors** — including trailing garbage and an empty file.
* **Unknown top-level entries** — with a did-you-mean suggestion when a known
  key is within edit distance 3, otherwise the full list of valid entries.
* **Unknown nested entries** (inside `models` objects, `enabled_tools`
  objects, …).
* **Wrong-typed values** — e.g. a string where an object is required.
* **Invalid enum values** (`permission-mode`).
* **Invalid boolean synonyms** for `boolean-with-synonyms` entries.

Every error renders the exact file, 1-based line, and 1-based column (columns
count runes, so UTF-8 content reports the position your editor shows):

```
error in <full path> at line L, column C: <detail>
```

Real examples of each shape:

```
error in /Users/u/Library/Application Support/late/config.json at line 2, column 3: "save_subagent_history" is not a valid config.json entry. Did you mean "save_subagent_histories"?
```

```
error in /Users/u/Library/Application Support/late/config.json at line 7, column 5: "enabled_tools" must be an object, found a string
```

```
error in /Users/u/Library/Application Support/late/config.json at line 9, column 22: "yolo" is not a valid permission-mode value. Valid values are: ask-for-user-approval, i-promise-i-have-backups-and-will-not-file-issues
```

```
error in /Users/u/Library/Application Support/late/config.json at line 4, column 20: "actve" is not a valid boolean value for "save_subagent_histories". Accepted values are: true, on, enabled, enable, active, activated, yes, y, 1 (true) or false, off, disabled, disable, no, not, n, 0 (false); case-insensitive, surrounding whitespace allowed
```

A missing file is the one non-error: on a fresh install Late writes the default
config and starts normally.

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
  "permission-mode": "ask-for-user-approval",
  "openai_base_url": "http://localhost:8080",
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
      "model": "qwen3.6-35b-a3b"
    }
  ],
  "agent_models": {
    "orchestrator": "frontier",
    "coder": "local"
  },
  "save_subagent_histories": true,
  "theme": "late"
}
```

## Key reference

Types: `string`, `number`, `boolean-with-synonyms` (see the synonym table
above), `array`, `object`.

### Models and providers

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `models` | array | `[]` | Model registry for `/model` and `agent_models`; each entry is `{id, url, key, model}` (see below). |
| `agent_models` | object | `{}` | Maps agent roles (`orchestrator`, `researcher`, `coder`, …) to a `models` entry `id` (or, legacy, its model name); persisted by `/model`. |
| `openai_base_url` | string | `http://localhost:8080` | Base URL of the main OpenAI-compatible API; `OPENAI_BASE_URL` env overrides when set. |
| `openai_api_key` | string | `""` | API key for the main provider; `OPENAI_API_KEY` env overrides when set. |
| `openai_model` | string | `""` | Main model id used when no `models`/`agent_models` routing applies; `OPENAI_MODEL` env overrides when set. |
| `late_subagent_base_url` | string | `""` (inherits main) | Dedicated subagent base URL; wins over the legacy `subagent_base_url`; `LATE_SUBAGENT_BASE_URL` env overrides when set. |
| `late_subagent_api_key` | string | `""` (inherits main) | Dedicated subagent API key; wins over the legacy `subagent_api_key`; `LATE_SUBAGENT_API_KEY` env overrides when set. |
| `late_subagent_model` | string | `""` (inherits main) | Dedicated subagent model; wins over the legacy `subagent_model`; `LATE_SUBAGENT_MODEL` env overrides when set. |

### Subagents

| Key | Type | Default | CLI flag | Description |
| --- | --- | --- | --- | --- |
| `save_subagent_histories` | boolean-with-synonyms | `false` | `--save-subagent-histories` | Persist subagent conversation histories under `<sessions>/<session-id>/subagents/` (a per-session saved preference sits between the flag and this entry). |

### Supervision

| Key | Type | Default | CLI flag | Description |
| --- | --- | --- | --- | --- |
| `permission-mode` | string | `"ask-for-user-approval"` | `--ask-for-user-approval` / `--i-promise-i-have-backups-and-will-not-file-issues` | How dangerous commands are supervised: one of the two mode values, each also a CLI flag; the flags override config and are mutually exclusive; an invalid value is a fatal strict-parsing error. |

### TUI and paths

| Key | Type | Default | CLI flag | Description |
| --- | --- | --- | --- | --- |
| `theme` | string | `""` | `--theme` | Plugin theme id (`plugin:name` or bare name); precedence `--theme` > `LATE_THEME` env > this entry > bundled base; persisted by `/themes`. |
| `skills_dir` | string | `""` | — | Legacy entry, currently unused: skill discovery reads the platform skills directory (`~/.config/late/skills/` etc.) and project-local `.late/skills/`, not this value. |
| `enabled_tools` | object | all built-in tools `true` | — | Per-tool switches (see below); missing entries are filled from the defaults. |

### Legacy entries

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `subagent_base_url` | string | `""` | Legacy subagent base URL; superseded by `late_subagent_base_url`, which wins when both are set. |
| `subagent_api_key` | string | `""` | Legacy subagent API key; superseded by `late_subagent_api_key`. |
| `subagent_model` | string | `""` | Legacy subagent model; superseded by `late_subagent_model`. |

## Nested schemas

### `models` entries

Each element of the `models` array is an object:

* `id` (string, optional) — stable identifier referenced by `agent_models` and
  the `/model` picker; omit it to fall back to the model name.
* `url` (string, required) — OpenAI-compatible base URL.
* `key` (string, required, may be `""`) — API key; local servers need none.
* `model` (string, required) — the model name the provider serves.

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
