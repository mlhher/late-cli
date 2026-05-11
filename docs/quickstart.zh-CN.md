# Late 快速入门指南

[English](quickstart.md) | [简体中文](quickstart.zh-CN.md)

本指南将帮助你在 5 分钟内快速上手并高效使用 Late。

## 初始设置

**1. 配置你的 API 端点** (支持任何兼容 OpenAI 格式的 API，例如 llama.cpp, [DeepSeek](https://api-docs.deepseek.com/), [阿里云百炼/通义千问](https://help.aliyun.com/zh/model-studio/compatibility-of-openai-with-dashscope), [Google](https://ai.google.dev/gemini-api/docs/openai), [Anthropic](https://platform.claude.com/docs/en/api/openai-sdk), [OpenRouter](https://openrouter.ai/docs/quickstart))：

```bash
# 本地模型 (如 llama.cpp)
export OPENAI_BASE_URL="http://localhost:8080"

# 云端模型 (如 DeepSeek)
export OPENAI_BASE_URL="https://api.deepseek.com/"
export OPENAI_API_KEY="你的-API-Key"
export OPENAI_MODEL="deepseek-v4-pro"
```

> **Windows 系统：** 请使用你所选终端的语法来设置环境变量。例如，在 PowerShell 中为 `$env:OPENAI_BASE_URL="http://localhost:8080"`。

**2. 在项目目录中启动 Late：**

```bash
cd your-project
late
```

> **macOS：** 如果 macOS 拦截了该二进制文件，请在终端中运行以下命令（必要时调整文件路径）：`xattr -d com.apple.quarantine ~/.local/bin/late`

**3. 混合模型路由 (可选)：**
默认情况下，Late 的主控节点（架构师）和临时工作节点（子智能体）会使用同一个模型。你可以通过设置独立的环境变量，让它们分别使用不同的模型。
如需永久保存这些设置，请查看 [配置说明](#配置说明) 部分。

如果你希望主控节点使用聪明的大模型来制定计划，而子智能体使用快速便宜的小模型来执行代码编写，这一功能将非常有用：

```bash
export LATE_SUBAGENT_MODEL="gemma-4-e4b"
export LATE_SUBAGENT_BASE_URL="http://10.8.0.2:8080" # (可选) 默认回退至 OPENAI_BASE_URL
export LATE_SUBAGENT_API_KEY="你的-另一个-API-Key"  # (可选) 默认回退至 OPENAI_API_KEY
```

## 用户界面

Late 提供了一个基于终端的用户界面 (TUI)，包含三个区域：**聊天视图**（可滚动的历史记录）、**输入框**（底部）以及 **状态栏**（显示当前模式、状态、token 计数及可用的快捷键）。

### 快捷键

| 按键 | 动作说明 |
| --- | --- |
| `Enter` | 发送当前消息（`Alt+Enter` 用于换行） |
| `↑` `↓` `PgUp` `PgDn` | 滚动聊天视图 |
| `Home` / `End` | 将光标移至行首/行尾（如果输入框为空，则用于滚动聊天视图至顶部/底部） |
| `Shift+Home/End` | 滚动聊天视图至最顶端/最底端 |
| `Tab` | 在不同智能体的标签页间切换（主控 ↔ 子智能体） |
| `Ctrl+A` | 打开文件选择器以附加文件 |
| `Ctrl+X` | 清除所有已附加的文件 |
| `Esc` / `Ctrl+G` | 停止当前智能体的运行（取消文本生成） |
| `Ctrl+D` / `Ctrl+C` | 退出 Late |

> **提示：** Late 支持标准的终端文本编辑快捷键，如 `Alt+Arrows`（按词跳跃）、`Ctrl+A/E`（行首/行尾）以及 `Alt+Backspace/Del`（删除整个词）。

### 文件附件

按 `Ctrl+A` 打开文件选择器。使用方向键浏览，按 `Enter` 选择文件或进入文件夹，按 `Backspace` 返回上级目录，按 `Esc` 取消选择。

- **文本文件**（源代码、配置文件、日志等）会作为内联内容附加，所有模型均可使用。
- **图片文件**（PNG、JPEG 等）仅在模型支持视觉功能时可用。Late 通过检测文件的实际内容（而非扩展名）来判断文件类型。如果你尝试向不支持视觉功能的模型附加图片，Late 会显示错误提示并拒绝该操作。

已附加的文件会在状态栏中以绿色计数器显示。发送前可按 `Ctrl+X` 清除所有附件。发送消息后，附件会自动清除。

### 智能体标签页 (Agent Tabs)

当 Late 生成子智能体时，每个子智能体都会拥有独立的标签页。使用 `Tab` 键即可在它们之间循环切换：

- **Main** — 主控节点（首席架构师）。负责制定计划并下达任务。
- **子智能体标签** — 负责执行独立任务的临时节点。它们在任务开始时生成，在任务完成后自动销毁。

底部的状态栏会显示你当前正在查看哪一个智能体以及它的工作状态（如 Idle 空闲, Thinking 思考中, Streaming 输出中等）。

> **提示：** 如果某个子智能体似乎卡住了，你可以按 `Tab` 切换到它查看具体情况。按 `Esc` 或 `Ctrl+G` 可以随时停止它的运行，这不会影响主控节点的工作。

## 如何编写高质量的指令

Late 在指令明确、具体的场景下表现最佳。例如：

```
# 好的示例
在 api/users.go 的 CreateUser handler 中添加输入验证。
检查 email 和 name 字段是否为空，如果为空则返回 400 状态码并附带 JSON 错误信息。

# 好的示例
重构 database 包以使用连接池。
连接池的配置应当从环境变量中读取。

# 糟糕的示例
把这部分代码改得更好。
```

Late 会自动读取你的代码库，生成实施计划，并请求你的批准。在点击批准前，请务必查看生成的计划文件 (`./implementation_plan.md`) 及拟议的修改。

## 工具授权审批

当智能体尝试运行命令或修改文件时，你会看到一个确认提示：

```
The agent wants to execute a bash command.
   {"command":"npm run build"}
> Press [y] Allow once | [s] Allow always (session) | [p] Allow always (project) | [g] Allow always (global) | [n] Deny
```

- **只读命令** (`ls`, `cat`, `grep` 等) 为了提高效率会被自动批准（注意：如果 Late 认为智能体的行为可疑，即使是只读命令也会被拦截要求授权）。
- **其他所有操作** 均需要你明确授权批准。
- 按 **`[y] Allow once`**：仅批准当前一次操作。
- 按 **`[s] Allow always (session)`**：在当前会话的剩余时间内，自动批准所有匹配的同类请求。
- 按 **`[p] Allow always (project)`**：在当前项目中记住此项授权。
- 按 **`[g] Allow always (global)`**：在当前机器上的所有项目中记住此项授权。
- 按 **`[n] Deny`**：拒绝该操作。

这种机制既能保证一次性操作的安全性，又能在你信任某项工具时减少繁琐的重复授权。

> **提示：** 授权快捷键（`y`, `n` 等）只有在输入框为 **空** 时才生效。如果你已经在输入消息，Late 会优先处理你正在输入的文本。你可以先发送你的消息（消息会被 **放入队列** 并在工具执行完毕后处理），然后在输入框清空时再使用单键授权。

### 权限衰减 (TTL)

“始终允许” (Always) 并非永久有效。Late 采用了 TTL (生存时间) 机制，信任度会随时间自动衰减：

- **Session (会话) 级别** (`[s]`) 持续 **30 分钟**。
- **Project (项目) 级别** (`[p]`) 持续 **30 天**。
- **Global (全局) 级别** (`[g]`) 持续 **30 天**。

当 TTL 过期时，先前的授权将自动失效，Late 会再次向你发起提示。这是有意设计的机制：既能清理长期搁置的无效授权，又能保障日常工作流的顺畅。

注意：
- 在同一作用域内重新批准某个工具/命令会重置其 TTL 倒计时。
- 会话级别的授权存在于内存中，设计上过期很快。
- 项目/全局级别的授权会带有过期时间戳持久化保存，并在启动时检查。

## 配置说明

你可以将偏好的模型选择（主控节点、子智能体）及其对应配置（主机 URL、API 密钥）永久保存在 `config.json` 文件中。

**文件存放位置：**
* **Linux:** `~/.config/late/config.json`
* **macOS:** `~/Library/Application Support/late/config.json`
* **Windows:** `%APPDATA%\late\config.json`

**配置优先级：**
1. 非空的环境变量
2. `config.json` 配置文件
3. 默认值

```json
{
  "openai_base_url": "http://localhost:8080",
  "openai_api_key": "your-api-key",
  "openai_model": "qwen3.6-35b-a3b",
  "late_subagent_base_url": "http://10.8.0.2:8080",
  "late_subagent_api_key": "your-other-api-key",
  "late_subagent_model": "gemma-4-e4b"
}
```

## MCP 协议集成

Late 支持 Model Context Protocol (大模型上下文协议)。请将你的 MCP 服务器配置添加至以下任意位置：

* **全局 (Linux):** `~/.config/late/mcp_config.json`
* **全局 (macOS):** `~/Library/Application Support/late/mcp_config.json`
* **全局 (Windows):** `%APPDATA%\late\mcp_config.json`
* **项目本地:** `.late/mcp_config.json`

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

## 智能体技能 (Agent Skills)

[Skills](https://agentskills.io/) 是一组可复用的指令集合。Late 会从以下目录自动发现并加载技能：

* **全局 (Linux):** `~/.config/late/skills/`
* **全局 (macOS):** `~/Library/Application Support/late/skills/`
* **全局 (Windows):** `%APPDATA%\late\skills\`
* **项目本地:** `.late/skills/`

无需其他任何配置，只需将你的技能文件放置在这些目录中，Late 就会自动识别并启用它们。

## 常用命令行标志 (Flags)

| Flag | 描述说明 |
| --- | --- |
| `--help` | 显示所有标志及可用命令 |
| `--version` | 显示当前版本信息 |
| `--gemma-thinking` | 为 Gemma 4 等模型注入专用的思考标记 (thinking tokens) |
| `--subagent-max-turns <n>` | 设置每个子智能体的最大交互轮数 (默认：500) |
| `--append-system-prompt "..."` | 向系统提示词的末尾追加文本（例如自定义的补充说明） |

## 会话管理 (Sessions)

Late 会自动保存你的聊天会话历史。你可以通过以下命令恢复或管理历史会话：

```bash
late session list          # 列出所有已保存的会话
late session list -v       # 列出详细信息的会话记录
late session load <id>     # 恢复指定的历史会话
late session delete <id>   # 删除指定的会话
```

## Git 工作树 (Git Worktrees)

Late 是专为并行开发设计的。你可以直接管理 Git 工作树，从而在隔离的环境中运行多个独立的智能体实例：

```bash
late worktree list               # 列出所有 Git 工作树
late worktree active             # 显示当前激活的工作树
late worktree create <path> [br] # 在 <path> 路径下创建一个新的工作树
late worktree remove <path>      # 移除一个工作树
```

> **提示：** 当你希望 Late 在后台独立负责一个功能分支的工作，而你同时要在另一个分支进行自己的开发时，使用工作树将是非常完美的选择。
