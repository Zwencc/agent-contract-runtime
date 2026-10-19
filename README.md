# Agent Contract Runtime（ACR）

[简体中文](README.md) | [English](README_EN.md)

> 跨Agent运行时记忆修正。

## 你是否遇到过这些情况？

- 明明前两天已经说过“不能修改这个数据库”，今天 AI 却又悄悄改了？
- 一个项目需要多个 AI 一起开发，但每换一个 AI，都要重新解释一遍什么能动、什么不能动？
- 上一个 AI 已经开发并验证过的功能，换一个 AI 后又被重新分析、甚至重新实现了一次？
- AI 明明知道项目约束，真正执行工具时却还是改错文件、漏跑测试，或直接触碰生产环境？
- 你纠正过一次错误，但这条纠正只停留在当前对话，下一次会话又从头踩坑？

ACR 希望让这些重复解释和失控修改不再依赖人的记忆。它把项目约束编译成可执行契约，在 Claude Code、Codex 等 Agent 之间共享经过证据确认的项目记忆，并在操作发生前完成放行、改写、修正、询问或拒绝。

**一次说明，多 Agent 生效；一次纠正，后续会话继承；每次操作，都有证据可以回放。**

## 它解决什么问题

- **跨 Agent 连续性**：项目规则、有效记忆和已完成工作不再绑定某一次对话。
- **操作前修正**：优先把错误目标和缺失步骤修正为合规操作，而不是等出错后再提醒。
- **可执行契约**：把“不要改生产库”“部署前必须测试”等自然语言约束变成确定性规则。
- **证据化记忆**：记忆带有来源、作用域、版本和生命周期，可以确认、纠正、争议、撤回和归档。
- **可回放审计**：意图、工具输入、修正过程、执行结果和验证证据进入追加事件链。
- **状态关系图**：从任务出发，只展开相关代码、契约、记忆、失败和验证证据。
- **依赖校准**：衡量人是否恰当地接受正确建议、拒绝错误建议，帮助识别对 AI 的过度依赖或过度拒绝。

## 产品界面

### 运行总览

在一个页面查看契约、修正成功率、记忆健康度、覆盖缺口、强制边界和跨 Agent 连续性。

![ACR 运行总览](.github/assets/acr-overview.png)

### 契约工作台

集中查看、模拟、审阅和激活项目约束；模型可以生成候选，但不能绕过人工确认直接激活高风险契约。

![ACR 契约工作台](.github/assets/acr-contracts.png)

### 项目状态图

以证据为锚点展示代码、契约、记忆、验证计划、Agent 会话和失败之间的关系，避免每次都全量分析仓库。

![ACR 项目状态图](.github/assets/acr-project-graph.png)

### AI 依赖校准

ACR 根据“建议 + 人类决定 + 已验证结果”计算恰当接受、恰当拒绝和过度依赖，并按操作风险分层展示。未知结果始终保持未知；该功能不是心理或医学诊断，也不会向真实项目注入错误，练习模式只使用固定沙箱数据。

![ACR 依赖校准](.github/assets/acr-reliance-calibration.png)

## 工作原理

```text
用户约束 / 项目规则
          │
          ▼
    可执行契约与记忆
          │
          ▼
Agent 操作 ──► PASS / REWRITE / REPAIR / ASK / DENY
          │
          ▼
  事件证据链与状态投影
          │
          ▼
下一次会话 / 另一个 Agent 的最小相关上下文
```

ACR 默认本地运行。确定性规则和验证器负责安全边界；可选模型只能生成候选、摘要或分类结果，不能直接激活契约、改变记忆状态或降低运行时权限。

## 快速开始

需要 Go 1.27 或更高版本。

```bash
go test ./...
go build -o ./bin/acr ./cmd/acr

# 启动本地控制台
./bin/acr ui --root /path/to/existing-project

# 扫描并初始化现有项目
./bin/acr init --root /path/to/existing-project --dry-run
./bin/acr bootstrap sources --root /path/to/existing-project

# 预览 Claude Code 与 Codex Hook 配置
./bin/acr install-hooks --root /path/to/existing-project --agent all --dry-run

# 验证示例契约
./bin/acr contract validate examples/contracts
```

运行控制台后访问 `http://127.0.0.1:7480`。ACR 只监听回环地址，运行数据默认保存在项目本地 `.acr/` 目录。

## 决策模型

| 决策 | 含义 |
| --- | --- |
| `PASS` | 操作符合当前有效契约，直接执行 |
| `REWRITE` | 可以确定性地修正工具输入后继续 |
| `REPAIR` | 先运行受控修复或补齐验证证据，再重新评估 |
| `ASK` | 无法安全推断，需要用户明确确认 |
| `DENY` | 操作违反不可绕过的高风险约束 |

## 文档

- [总体架构](docs/02-architecture.md)
- [可执行契约规范](docs/03-contract-spec.md)
- [事件、状态图与上下文模型](docs/04-event-and-state-model.md)
- [安全与威胁模型](docs/07-security-and-threat-model.md)
- [跨 Agent 记忆生命周期](docs/08-memory-lifecycle.md)
- [让 ACR 真正干活](docs/11-practical-runtime.md)
- [中途接入现有项目](docs/12-bootstrap-existing-project.md)
- [未来功能](docs/14-future-capabilities.md)

## 支持的 Agent

- Claude Code：原生 Hook、会话上下文、工具前后事件和 Guarded 模式。
- Codex：工具钩子、MCP 指令通道、会话上下文和 Guarded 模式。

Native Hook 提供可观测和操作前决策能力；Guarded 模式通过受控工具通道提供更强边界。ACR 不宣称能够对本机管理员形成不可绕过的绝对强制。

## 隐私与安全

- 默认本地存储，不要求云端同步。
- 事件、导出和修复输出进入存储前经过敏感信息脱敏。
- 模型输出只能形成候选，不能自行提升权限。
- 事件日志使用项目级序号和 SHA-256 哈希链提供可验证的篡改证据。
- 控制台只允许回环地址访问，并使用本地会话令牌。

## 许可证

Apache License 2.0，见 [LICENSE](LICENSE)。贡献方式见 [CONTRIBUTING.md](CONTRIBUTING.md)，安全问题请按 [SECURITY.md](SECURITY.md) 私密报告。
