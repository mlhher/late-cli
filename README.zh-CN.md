# Late: 高效的 AI 智能体编排工具 (High-Leverage AI Agent Orchestration)

[English](README.md) | [简体中文](README.zh-CN.md)

> 现有的编程智能体常常将编辑、重试和实现细节全部塞进自己的上下文窗口中，直到模型彻底“迷失”。而 Late 则将这些细节委托给短暂的“子智能体 (subagents)”处理——它们在隔离的上下文中执行单一任务，并在完成后自动销毁。主控节点 (Orchestrator) 仅负责制定计划和查看结果，绝不干涉具体的代码实现细节。单一静态文件，零依赖，支持任何模型。

[![Release](https://img.shields.io/github/v/release/mlhher/late-cli)](https://github.com/mlhher/late-cli/releases) [![Homebrew](https://img.shields.io/badge/Homebrew-tap-brightgreen.svg)](https://github.com/mlhher/homebrew-late) [![Go Report Card](https://goreportcard.com/badge/github.com/mlhher/late)](https://goreportcard.com/report/github.com/mlhher/late) [![DeepWiki](https://img.shields.io/badge/DeepWiki-docs-blue.svg)](https://deepwiki.com/mlhher/late-cli)

**随时随地投入项目，即刻开始构建。** 只需不到 10 秒钟即可发送你的第一条指令。

```bash
brew tap mlhher/late && brew install late
cd your-project
late
```

> **未使用 Homebrew?**
> - **Arch Linux:** `yay -S late-cli-bin`
> - **Linux / macOS / Windows:** 下载 [最新版本](https://github.com/mlhher/late-cli/releases) 并将其放入 PATH 环境变量中。*(macOS 手动下载: 如果被系统阻止，请执行 `xattr -d com.apple.quarantine /path/to/late`)*
> 
> **连接云端模型?**
> 默认情况本地模型 (如 `llama.cpp`，运行在 `:8080` 端口) 可开箱即用，无需配置。如果使用云端服务 (如 DeepSeek, 阿里云百炼, 通义千问, Claude, Gemini, OpenRouter 等)，请在运行前设置 `OPENAI_BASE_URL`，`OPENAI_API_KEY` 和 `OPENAI_MODEL` 环境变量。

![Late Orchestrator planning a multi-phase implementation and spawning the first subagent](assets/late-subagent-handoff.png)
*架构师 (主控节点) 正在制定计划，并生成原子化的子智能体以执行精确的代码修改。*

|  | Late | Claude Code | OpenCode | The Weekly Clone |
| --- | --- | --- | --- | --- |
| **工作流** | **自主编排** | 手动切换 | 手动切换 | 盲目执行 / 手动切换 |
| **代码实现** | **临时子智能体 (上下文销毁)** | 充斥主上下文窗口 | 充斥主上下文窗口 | 充斥主上下文窗口 |
| **KV-Cache** | **严苛的 KV 缓存管理** | “反正公司报销 API 账单” | 暴力上下文堆砌 | 暴力上下文堆砌 |
| **系统提示词** | **~1,000 tokens (始终处于计划工作流)** | 10,000+ tokens | 10,000+ tokens | ~300-1000+ tokens (无工作流的阉割版) |
| **依赖** | **零依赖静态二进制文件** | Node.js | Node.js | Python / Node.js |
| **安装要求** | **无 (原生支持 `llama-server`)** | Anthropic OAuth / 登录 | 强制 JSON 调整 | 朝令夕改的 JSON/YAML/TOML 配置 |
| **设计初衷** | **追求 10 倍效率的开发者** | 企业级报销大户 | 摆弄各种设置 | 追求 GitHub Stars |

> *"同样的模型，在 Late 里感觉更聪明了。"* — Reddit

> *"Late-CLI 简直令人惊叹…… 我震惊于它的 Token 消耗竟如此之低，我总觉得自己会收到 DeepSeek 的天价账单。"* — GitHub Discussions

> [在本地 LLM 工作流中超越 Claude Code 和 Codex](https://agentnativedev.medium.com/outperforming-claude-code-and-codex-for-local-llm-workflows-5de0e2b1add5) — Agent Native

> **使用 Late 构建:** Late 主要是由 Late 自己辅助开发完成的。

原生支持 **DeepSeek, Qwen (通义千问), Claude, Gemma (支持思考过程)** 以及任何兼容 OpenAI 格式的 API 接口。请参阅 [快速入门指南](docs/quickstart.zh-CN.md) 了解混合模型路由、快捷键、MCP 设置及 Skills 扩展等高级功能。

---

## 工作原理

标准编程智能体在同一个共享上下文窗口中完成所有工作，无论是规划、实现、重试失败的代码修改，还是自我修复。每次重试、每次失败的实现、每次修复循环都在污染模型进行推理的上下文。模型性能随之下降，你往往认为是模型不够好。其实模型没问题，问题出在架构上。

Late 采用了分离关注点的设计。一个精简的主控节点（系统提示词约 1,000 个 tokens）负责读取代码库、制定计划，并将具体的实现任务分配给短暂的子智能体。每个子智能体都会获得一个全新的、隔离的上下文，其中仅包含分配给它的单一任务，没有其他任何杂质。当任务完成时，该上下文就会被销毁。主控节点只会看到最终的结果。

Late 极其谨慎地管理 KV 缓存和上下文窗口，为逻辑推理留出更多空间。主控节点的上下文只会因为真正重要的信息而增长：即你的指令和智能体做出的决策。子智能体为了完成任务所做的一切中间过程，都会随其一同销毁。这就是为什么同样的模型在 Late 中表现得更敏锐：它是基于“信号”而非“噪音”进行推理的。

---

## 核心特性

- **混合模型路由 (Hybrid Model Routing):** 使用强大的推理模型 (例如 DeepSeek V4) 制定计划，然后生成子智能体使用极速且廉价的本地模型 (例如 Gemma 4) 来执行。
- **精准差异对比 (Exact-match Diffs):** 采用严格的“搜索/替换”逻辑，并在匹配失败时实现自动自我修复。编辑失败会明确报错，绝不默默破坏文件。
- **人机协作 (Human-in-the-loop):** 读取操作自动批准以提高效率，代码修改强制拦截等待 `[y/N]` 确认。支持会话级、项目级和全局级的信任授权，并带有 TTL 自动衰减机制。
- **状态持久性 (Stateful Resilience):** 主控节点将连续的会话历史记录保存在磁盘上。即使关闭终端或重启机器，也能从上次中断的地方继续。
- **MCP 协议集成 (MCP Integration):** 通过标准 I/O 原生连接外部模型上下文协议 (Model Context Protocol) 服务器。
- **智能体技能 (Agent Skills):** 直接放入可重用的指令集和脚本，无需任何繁琐配置或样板代码。
- **Git 工作树支持 (Git Worktree Support):** 支持跨分支的并行独立智能体实例，而不会出现上下文冲突。
- **Gemma 4 思考模式 (Gemma 4 Thinking Mode):** 标准套壳工具只会把文本死板地丢给 API，导致根本无法触发 Gemma 的推理功能。Late 提供了一个专属 Flag，用于精确注入触发其思考模式所需的特定 Token。

---

## 开源协议与声明

本项目旨在为个人和团队创造工程杠杆，而非为 AI 初创公司提供免费的底层基础设施。

**对开发者完全免费:** 任何开发者都可以自由使用 Late 来为任何项目（包括商业项目）编写代码。你使用该工具生成的产出完全归属于你。

**商业限制:** 你不得对 Late 本身进行商业化变现——将该编排引擎封装成付费服务，或将其作为企业基础设施进行部署，需要事先获得商业许可协议。

Late 将于 2030 年 2 月 21 日自动转为 GPLv2 协议。完整许可详情请见 [LICENSE](LICENSE)。
