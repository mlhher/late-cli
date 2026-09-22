# Late 快速入门指南

[English](quickstart.md) | [简体中文](quickstart.zh-CN.md)

只需几分钟，即可完成从安装到执行你的第一个自主编程任务。

## 安装

### Homebrew — Linux / macOS

```bash
brew tap mlhher/late && brew install late
```

### 通用安装方式 — Linux / macOS / Windows WSL

```bash
curl -sfL https://raw.githubusercontent.com/mlhher/late-cli/main/install.sh | bash
```

[GitHub Releases](https://github.com/mlhher/late-cli/releases) 中提供了 Linux、macOS 和原生 Windows 的手动下载版本。

---

## 本地模型：零配置

Late 会自动寻找运行在 `localhost:8080` 上的兼容 OpenAI 的 `llama-server`。

正常使用 GGUF 模型启动 `llama-server`，然后在你的项目中启动 Late：

```bash
cd your-project
late
```

就是这样。如果 `llama-server` 已经在 `:8080`（其默认端口）上运行，Late 会在不需要任何配置的情况下自动连接。

---

## 云端模型

Late 支持任何兼容 OpenAI 的 API，包括 DeepSeek、Claude、GPT、Kimi、GLM、OpenRouter 等。

设置环境变量：

```bash
# DeepSeek 示例
export OPENAI_BASE_URL="https://api.deepseek.com" # 你的 API 地址
export OPENAI_API_KEY="sk-123" # 你的 API 密钥
export OPENAI_MODEL="deepseek-flash" # 模型名称
```

然后运行：

```bash
cd your-project
late
```

稍后你可以将这些模型设置持久化保存在 Late 的 `config.json` 中，而不需要每次都导出环境变量。

---

## 配置

持久化配置文件位于：

* **Linux:** `~/.config/late/config.json`
* **macOS:** `~/Library/Application Support/late/config.json`
* **Windows:** `%APPDATA%\late\config.json`

配置的优先级顺序为：

1. 环境变量
2. `config.json`
3. Late 默认值

对于在 `localhost:8080` 上运行的标准本地 `llama-server`，你不需要创建配置文件。

### 工具授权模式（`permission-mode`）

你可以通过在所用平台对应的 `config.json`（位置见上文）中添加 `permission-mode` 条目，来选择 Late 对危险命令的监督程度：

```json
{
  "permission-mode": "ask-for-user-approval"
}
```

有两个可选值：

* `ask-for-user-approval` — 默认值。可能具有破坏性的命令需要你的批准。
* `i-promise-i-have-backups-and-will-not-file-issues` — 运行所有工具而不需要用户确认。

注意事项：

* 两个同名的 CLI 标志（`--ask-for-user-approval`、`--i-promise-i-have-backups-and-will-not-file-issues`）互斥，且会覆盖 `config.json` 中的值。
* 省略该条目（以及任何标志）时，默认为 `ask-for-user-approval`。
* 无效的值会被忽略并给出警告，同时应用安全的默认值。

### 高级模型配置（`models` 和 `agent_models`）

默认情况下，Late 为主编排器和子智能体使用同一个模型。不过，你可以将不同的模型映射到特定的智能体角色（例如，使用庞大的前沿模型进行规划，使用快速的本地模型进行执行）。

你可以在 TUI 中使用 `/model` 交互式地切换模型，或者通过 `models` 注册表将你的混合路由持久化保存在 `config.json` 中：

```json
{
  "models": [
    {
      "id": "deepseek",
      "url": "https://api.deepseek.com",
      "key": "sk-your-key",
      "model": "deepseek-flash"
    },
    {
      "id": "local-qwen",
      "url": "http://localhost:8080",
      "key": "",
      "model": "qwen3.6-35b-a3b"
    },
    {
      "id": "local-gemma",
      "url": "http://localhost:8080",
      "key": "",
      "model": "gemma-4-e4b"
    }
  ],
  "agent_models": {
    "orchestrator": "deepseek",
    "researcher": "local-qwen",
    "coder": "local-gemma"
  }
}
```

> 注意：为了保持向后兼容，旧的扁平化格式（如 `openai_base_url`、`late_subagent_model` 等）仍然受支持。

---

## TUI（终端用户界面）

Late 将编排器和活动中的子智能体展示在同一个终端界面中。

基本操作：

| 快捷键 / 命令       | 动作                                               |
| ------------------- | ---------------------------------------------------- |
| `Tab`               | 在编排器和活动中的子智能体之间切换 |
| `Ctrl+O`            | 附加文件                                        |
| `Esc` / `Ctrl+G`    | 停止当前正在运行的智能体                     |
| `/model`            | 更改编排器或 Worker 的模型                 |
| `/rewind`           | 回溯到对话中更早的状态       |
| `/themes`           | 更改 TUI 主题                                 |
| `/compose`          | 在你的 `$EDITOR` 中编写长指令         |
| `Ctrl+D` / `Ctrl+C` | 退出                                                 |

在任何时候输入 `/` 即可打开命令选择器。

当 Late 创建子智能体时，它们会在工作期间显示在独立的标签页中，并在完成任务后消失。

---

## 工具执行授权

除非你已经为该范围授予了权限，否则可能具有破坏性的命令和文件更改都需要你的批准。

当弹出提示时，你可以批准：

* 仅限这一次；
* 对于当前会话；
* 对于当前项目；
* 全局允许。

只读操作通常会自动处理。

授权会随着时间的推移而衰减，而不会成为永久信任。

---

## 使用 Podman 运行完全自主的工作流

对于无人值守的任务、大规模重构或过夜运行，请使用 `late-podman`。

它会在一个隔离的、rootless 的 Podman 容器中运行 Late，而不是让智能体对你的主机拥有不受限制的访问权限。

在项目内部运行：

```bash
late-podman
```

Late 会自动寻找项目中的容器配置，包括 `.devcontainer/devcontainer.json`。如需参考示例，请查看 Late 自带的 [devcontainer.json](../.devcontainer/devcontainer.json)。

你也可以明确指定一个镜像：

```bash
late-podman --image your-development-image
```

`--` 之后的参数会被传递给 Late：

```bash
late-podman -- --continue
```

你当前的工作区会以读写模式挂载到 `/workspace`。Late 维护自己的会话和缓存卷，在可用时转发你的 SSH agent，并以只读模式挂载你的 Late 配置。默认情况下，主机的主目录和容器 socket 不会被暴露。

> **注意：** `late-podman` 需要安装了 Podman 的 Linux。它包含额外的 SELinux 支持，并在 Silverblue 和 Universal Blue 镜像上开箱即用。

> **注意：** Devcontainer 配置可以声明额外的挂载。Late 在构建沙箱时会遵守这些配置。

---

## 预设 Prompt 启动

适用于脚本或无人值守工作流：

```bash
late --prompt "Run the test suite, diagnose the failures, and fix them."
```

如果使用 `late-podman`：

```bash
late-podman -- --prompt "Refactor this package and verify all tests."
```

---

## 恢复先前的工作

Late 会自动保存会话。

恢复最近更新的会话，无论它属于哪个项目：

```bash
late --continue
```

恢复**当前项目**中最近更新的会话。项目会解析为包含当前工作目录的 git 仓库根目录（不在仓库中时回退为当前工作目录本身），因此在子目录中同样有效：

```bash
late --continue-project
```

这两个标志互斥：最多只能传入一个。其他项目中的会话——或在此功能引入之前创建的会话——可以使用 `late session list` 查找（使用 `-v` 查看每个会话的项目目录），并使用 `late session load <id>` 恢复：

```bash
late session list -v
late session load <id>
```

---

## MCP 集成

Late 支持 Model Context Protocol (MCP)，允许你接入自己的外部工具。将你的 MCP 服务器添加到以下位置之一：

* **Linux:** `~/.config/late/mcp_config.json`
* **macOS:** `~/Library/Application Support/late/mcp_config.json`
* **Windows:** `%APPDATA%\late\mcp_config.json`
* **项目局部:** `.late/mcp_config.json`

```json
{
  "mcpServers": {
    "my-server": {
      "command": "npx",
      "args": ["-y", "my-mcp-server"]
    }
  }
}
```

---

## Agent Skills

[Skills](https://agentskills.io/) 是可复用的 Markdown 指令集。它们会从以下位置被自动发现：

* **Linux:** `~/.config/late/skills/`
* **macOS:** `~/Library/Application Support/late/skills/`
* **Windows:** `%APPDATA%\late\skills\`
* **项目局部:** `.late/skills/`

无需配置。只需将你的 skills 放入相应的文件夹，Late 就会自动加载它们。

---

## 插件

插件将 **skills**、**slash 命令**、**MCP 服务器**、**hooks**、**主题** 和 **内联工具** 打包成一个可安装的单元。

你可以在 https://github.com/mlhher/late-plugins 找到默认注册表。

```bash
# 从默认注册表或 npm 回退安装
late plugin install notify-tool-approval

# 从 Git 仓库安装
late plugin install https://github.com/you/late-plugin.git

# 从本地路径安装以进行开发
late plugin install ./my-plugin
```

关于插件开发和清单格式，请参阅 [Plugin SDK](plugin-sdk.md)。

---

## 文件排除

Late 的原生搜索工具会自动遵守你项目的 `.gitignore`，通过排除 vendor 和构建目录来节省 LLM 上下文。

你也可以在 `.gitignore` 旁边创建一个 `.llmignore` 文件，以专门对智能体隐藏文件（例如，机密信息、大型二进制文件、测试 fixtures 或生成的代码），而不会影响你的 git 追踪。

---

## 流式重试

瞬时的 LLM API 故障会被自动重试，因此不稳定的网关很少会中断一次运行：

* 传输类错误——连接被拒绝/重置、超时，以及流式中断（服务器已接受请求，但响应体随后断开）——会被重试。
* HTTP 408、429 和 5xx 响应会被重试；HTTP 400 会获得少量快速重试。
* 每次重试都会等待叠加抖动的指数退避（以 500 ms 为基数，上限 30 s）。服务器返回的 `Retry-After` 会被作为等待下限遵守，并设有 5 分钟上限。
* 重试无法解决的故障会立即失败：TLS/证书错误、不支持的 URL scheme，以及在 HTTPS 端点上使用纯 HTTP。

重试预算按 CLI 标志 > 环境变量 > 内置默认值 的优先级解析：

* `--max-stream-retries <n>` — 每次流式调用的最大重试次数（默认：10）
* `LATE_MAX_STREAM_RETRIES` — 环境变量，在未传入标志时生效

设置为 `0`（或负值）会完全禁用流式重试。运行 `late -h` 可查看所有标志。

---

## 常用标志 (Common Flags)

| 标志 | 描述 |
| --- | --- |
| `--help` | 显示所有标志和命令 |
| `--version` | 显示版本信息 |
| `--continue` | 恢复最近更新的会话，不限项目目录 |
| `--continue-project` | 恢复当前项目中最近更新的会话（解析为当前工作目录所在的 git 仓库根目录；不在仓库中时回退为当前工作目录） |
| `--prompt "..."` | 使用给定的 prompt 立即启动智能体 |
| `--suppress-thinking-words` | 应用关于过度思考词汇的默认 Logit 偏置映射（仅限 `llama.cpp`） |
| `--logit-bias` 和 `--subagent-logit-bias` | 手动设置特定模型的 logit 偏置（仅限 `llama.cpp`） |
| `--gemma-thinking` | 为 Gemma 4 模型注入思考 token |
| `--subagent-max-turns <n>` | 设置每个子智能体的最大轮次（默认：500） |
| `--append-system-prompt "..."` | 在系统 prompt 附加文本（如：额外指令） |
| `--enable-images` | 将模型视为支持图像（适用于非 llama.cpp 的服务器） |
| `--save-subagent-histories` | 将子智能体的对话记录持久化到磁盘 |
| `--max-stream-retries <n>` | 每次 LLM 流式调用的最大重试次数，采用叠加抖动的指数退避（默认：10）；`0` 表示禁用流式重试。环境变量：`LATE_MAX_STREAM_RETRIES` |
| `--ask-for-user-approval` | 要求对危险命令进行用户批准（默认值；覆盖 `config.json` 中的 `permission-mode`） |
| `--i-promise-i-have-backups-and-will-not-file-issues` | 运行所有工具而不需要用户确认（覆盖 `config.json` 中的 `permission-mode`） |

上面的两个权限标志互斥：`--ask-for-user-approval` 和 `--i-promise-i-have-backups-and-will-not-file-issues` 最多只能传入一个。
