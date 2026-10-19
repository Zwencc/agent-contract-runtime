# Agent Contract Runtime（ACR）

> 面向多种编码 Agent 的跨会话项目连续性与操作修正运行时。

ACR 的核心不是让 Agent 记住更多文字，也不是在违规后简单打断，而是把项目约束编译成可执行契约，在操作执行前尽可能将原始操作修正为合规操作，并用可验证、可回放的事件证据维持跨 Agent 连续性。

## 核心命题

跨 Agent 的连续性不只取决于 Agent “知道什么”，还取决于运行时能否保证：

1. 同一项目的有效约束会在 Claude Code、Codex 等不同 Agent 间一致生效。
2. 可安全确定修正的操作自动修正后继续执行。
3. 无法确定修正的高风险操作才进入询问或拒绝路径。
4. 已完成、失败和验证过的工作以原始证据保存，而不是只保存摘要。
5. 新会话只接收与当前任务相关的最小状态包，避免全量重复分析。

## 设计原则

- **修正优先**：`PASS -> REWRITE -> REPAIR -> ASK -> DENY`，拒绝是最后手段。
- **确定性优先**：安全边界由确定性规则和验证器决定；模型可以生成候选，不能自行激活高风险契约。
- **证据优先**：结论必须能追溯到事件、工具输入、工具结果或人工确认。
- **日志为事实源**：事件日志是事实源，项目状态图是可重建的派生视图。
- **记忆有生命周期**：记忆必须有来源、作用域、有效期和纠错路径；不是保存得越多越好。
- **分级保证**：明确区分原生 Hook 能力和受控执行环境能力，不宣称本地管理员无法绕过的“绝对强制”。
- **本地优先**：默认数据留在本机，联网模型、嵌入和同步均为可选能力。
- **真实工作优先**：以避免返工、完成修正和可靠交接衡量价值，不以记忆数量或拦截次数衡量。

## 文档导航

- [技术可行性评估](docs/01-feasibility-assessment.md)
- [总体架构](docs/02-architecture.md)
- [可执行契约规范](docs/03-contract-spec.md)
- [事件、状态图与上下文模型](docs/04-event-and-state-model.md)
- [Claude Code 与 Codex 适配设计](docs/05-agent-adapters.md)
- [开发计划与验收标准](docs/06-development-plan.md)
- [安全与威胁模型](docs/07-security-and-threat-model.md)
- [跨 Agent 记忆生命周期](docs/08-memory-lifecycle.md)
- [Hook 失效、人工修正与恢复](docs/09-hook-recovery-and-operations.md)
- [第二阶段与正式版本](docs/10-release-stages.md)
- [让 ACR 真正干活](docs/11-practical-runtime.md)
- [中途接入现有项目](docs/12-bootstrap-existing-project.md)

## 当前状态

当前仓库处于 v0.1 alpha 开发阶段，已经具备以下可运行闭环：

- Go 单二进制 CLI；
- 严格 JSON 契约加载与生命周期校验；
- PASS、REWRITE、ASK、DENY 确定性评估；
- `set_fields`、`remove_fields`、受限 `replace_path` 修正；
- 修正后全量二次评估与冲突检测；
- Claude Code、Codex `PreToolUse` 输入规范化和原生输出；
- Claude Code、Codex `PostToolUse` 结果记录及 Claude `PostToolUseFailure` 失败记录；
- Claude Code、Codex `SessionStart` 有预算的契约与近期证据上下文包；
- SQLite 追加事件、项目级序号、SHA-256 哈希链及完整性验证；
- `action.proposed -> action.*` 意图—结果审计链。
- 存量项目确定性 bootstrap：发现 Agent 指令、规则、ADR、项目文档和现有契约，限制扫描范围并保存来源摘要；
- SessionStart 增量来源对账：来源变化先写入追加事件，再推进 baseline，不自动激活旧文本；
- Hook 配置语义合并、默认预览、显式 `--apply` 原子写入及原文件备份。
- 结构化记忆候选、P0-P6 优先级、显式激活/确认/失效/争议/纠错/撤销/归档、不可变 revision 历史和 SessionStart 注入。
- 工作区覆盖哨兵：SessionStart 对账文件摘要；没有工具结果证据的变化生成 confirmed gap，无法关联到已有工具输入的变化生成 suspected gap。
- Guarded Repair v0：严格 JSON 多步计划、默认预览、显式执行、非隐式 shell argv、资源锁、幂等键、逐步事件、输出脱敏和崩溃后拒绝盲目重放。

尚未完成的生产能力包括真实宿主实时探针、自动 Memory Write Gate、记忆压缩、多步 Guarded Repair、跨文件 Hook 安装事务和发布工程，因此当前版本不能标记为生产就绪。

## 本地运行

```bash
go test ./...
go build -o ./bin/acr ./cmd/acr

./bin/acr ui --root /path/to/existing-project
./bin/acr contract validate examples/contracts
./bin/acr init --root /path/to/existing-project --dry-run
./bin/acr bootstrap sources --root /path/to/existing-project
./bin/acr install-hooks --root /path/to/existing-project --agent all --dry-run
./bin/acr reconcile status --root /path/to/existing-project
./bin/acr repair run --root /path/to/existing-project \
  --plan examples/repair/test-before-deploy.json
./bin/acr memory add --root /path/to/existing-project \
  --kind user_correction --priority P0 \
  --claim "不要修改生产数据" --evidence human:review
./bin/acr hook codex pre-tool-use \
  --contracts examples/contracts/codex-rewrite-generated-patch.json \
  --project-id example-project \
  --workspace-id example-workspace \
  --event-db /tmp/acr-example.db \
  < examples/hooks/codex-pre-tool-use.json
```

预期 Hook 输出中的 `permissionDecision` 为 `allow`，`updatedInput.command` 的补丁目标从 `generated/schema.go` 修正为 `src/schema.go`。该示例只用于无副作用协议验证；不要直接把 `/tmp` 示例配置当成生产配置。

`examples/contracts/` 中的示例契约覆盖五类规则：生成目录写入改写到源目录（`shared-generated-to-src`，用项目相对 `paths` 写成，Claude Code 与 Codex 共用一条）、直接操作生产库需人工确认、禁止改写 `.env` 类密钥文件、禁止强制推送、部署前测试证据（配合 `examples/repair/checks.json`，放到项目的 `.acr/plans/checks.json`）。`cmd/acr/scenario_test.go` 对两个 Agent 逐条验证它们的效果。

当前契约加载器只接受 `.json`。YAML、契约审批签名和 Schema 文件将在冻结 v0.1 线格式前补齐。

## 许可证

Apache License 2.0，见 [LICENSE](LICENSE)。贡献方式见 [CONTRIBUTING.md](CONTRIBUTING.md)，安全问题请按 [SECURITY.md](SECURITY.md) 私密报告。
