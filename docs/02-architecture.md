# ACR 总体架构

## 1. 架构目标

ACR 必须同时满足四个目标：

1. Claude Code 与 Codex 使用同一套契约语义。
2. 工具调用热路径足够快，普通检查不依赖网络或大模型。
3. 原始事实可回放，派生状态可重建。
4. 从个人本地使用平滑升级到团队受控环境，不拆成两套产品。

## 2. 逻辑架构

```text
Claude Code Hook ─┐
                  ├─> Adapter ─> Normalized Agent Event
Codex Hook ───────┘                      │
                                         v
                              Contract Evaluation Engine
                              ├─ applicability
                              ├─ policy evaluation
                              ├─ correction planning
                              └─ pre-verification
                                         │
                 ┌───────────────────────┼──────────────────────┐
                 v                       v                      v
              PASS/REWRITE          REPAIR/ASK/DENY       Context Packet
                 │                       │                      │
                 v                       v                      v
          Native Hook response    Guarded Executor       Agent context
                 │                       │
                 └───────────┬───────────┘
                             v
                        Event Store
                             │
                 ┌───────────┴───────────┐
                 v                       v
            State Projector       Candidate Distiller
                 │                       │
                 v                       v
          Project State Graph     Draft Contracts
```

## 3. 进程与模块

建议首版保持单仓库、单语言、单二进制：

```text
cmd/
  acr/                  CLI 入口
internal/
  adapter/
    claude/             Claude Code Schema 转换
    codex/              Codex Schema 转换
  contract/             契约模型、加载、版本与审批
  evaluator/            applicability、策略与优先级
  correction/           单步改写与多步修复计划
  verifier/             前置/后置验证器
  executor/             Guarded 模式执行代理
  eventstore/           追加事件、哈希链、对象引用
  projector/            状态图和物化视图
  contextpack/          任务相关上下文组装
  redaction/            写日志前脱敏
  daemon/               本地 IPC 服务
  protocol/             版本化内部协议
schemas/                 JSON Schema
adapters/                两端安装模板与兼容样例
docs/                    设计与用户文档
```

目录只描述目标结构，不代表当前已经实现。

## 4. 请求处理流水线

### 4.1 会话启动

1. Adapter 接收 `SessionStart`。
2. 解析项目身份、工作树身份、Agent 类型和原生会话 ID。
3. 写入 `session.started`。
4. 查询当前有效硬契约和最近项目状态。
5. 生成有字符/token 预算的上下文包。
6. 通过各 Agent 的上下文注入字段返回。

上下文包只用于帮助 Agent 提前做对；真正安全约束仍在工具执行前重新计算，不能只依赖启动注入。

### 4.2 工具执行前

1. Adapter 将原生 Hook 输入转换成 `ToolCallProposed`。
2. 脱敏器标记敏感字段，但保留策略判断所需的受控视图。
3. 计算适用契约集合。
4. 按优先级执行确定性策略。
5. 若需修正，生成 `CorrectionPlan`。
6. 对修改后的参数再次执行全部硬契约，防止修正引入新违规。
7. 返回 `PASS`、`REWRITE`、`REPAIR`、`ASK` 或 `DENY`。
8. 记录原始输入、有效输入、命中契约和决策理由。

必须保证修正后的操作重新走完整策略，不允许“修正结果天然可信”。为防止循环，每次调用设置最大修正次数，建议为 1；复杂链路转入 Guarded 模式。

### 4.3 工具执行后

1. Adapter 接收成功或失败结果。
2. 使用原生 `tool_use_id` 映射到提议事件。
3. 脱敏并保存结果摘要；大输出进入对象库。
4. 运行后置验证器。
5. 写入 `action.completed`、`action.failed` 或 `verification.failed`。
6. 更新状态投影。
7. 如检测到重复失败模式，只生成契约候选，不自动激活。

## 5. 决策与冲突规则

契约必须有稳定、可解释的合并语义：

1. 硬约束优先于软约束。
2. 更具体的 scope 优先于更宽泛的 scope，但不能覆盖被标记为 `non_overridable` 的上级硬约束。
3. 显式 `DENY` 优先于 `REWRITE` 和 `PASS`。
4. 多个改写同时命中时，只有证明可交换或有显式顺序才能组合。
5. 改写目标冲突时进入 `ASK`，高风险或无人值守模式进入 `DENY`。
6. 每次决策返回完整解释：适用契约、未适用原因、修改字段和验证结果。

## 6. 保证等级

| 等级 | 名称 | 能力 | 典型部署 |
| --- | --- | --- | --- |
| L0 | Context | 只注入上下文和记录事件 | 试用/观察 |
| L1 | Native Correction | 原生 Hook 单步改写与拒绝 | 个人开发 |
| L2 | Guarded Execution | 多步修复事务、统一 fail-closed | 团队项目 |
| L3 | Managed Boundary | 托管 Hook、沙箱和集中策略 | 企业受控环境 |

产品 UI、CLI 和事件中必须展示当前等级及降级原因。L1 不能宣传成 L2/L3。

## 7. 故障策略

每份契约声明 `failure_mode`：

- `open`：ACR 不可用时放行并告警，只适合提示性规则。
- `closed`：ACR 不可用、超时或输出非法时阻止执行，适合硬安全规则。
- `ask`：转人工确认；无人值守时等价于 `closed`。

但 Adapter 必须服从宿主能力。Codex 官方文档明确提示部分 Hook 错误、超时或非法响应可能不阻止工具，因此 Native 模式对 `closed` 的实现必须报告为“不满足”，建议用户切换 Guarded 模式，而不是制造虚假安全感。[Codex Hooks 官方文档](https://learn.chatgpt.com/docs/hooks)

Claude Code 支持在较新版本为命令/HTTP Hook 配置 `onFailure: "block"`，可增强 Native 模式的 fail-closed 行为，但仍不能防止用户停用配置。[Claude Code Hooks 官方文档](https://code.claude.com/docs/en/hooks)

## 8. 本地通信与性能预算

Hook 热路径建议：

- Adapter 到本地守护进程：Unix Socket / Named Pipe。
- 编码：单行 JSON，请求和响应均带协议版本。
- 纯确定性检查目标 P95 小于 30 ms，P99 小于 100 ms。
- 守护进程不可用时，可选单进程直接模式用于软契约；硬契约依据保证等级处理。
- 模型调用永远不进入默认热路径。

## 9. 项目身份与隔离

项目通过 `.acr/project.yaml` 中的随机 `project_id` 标识，路径和 Git remote 只作为发现线索：

```yaml
schema_version: acr.project/v1alpha1
project_id: 01JPROJECTEXAMPLE0000000000
name: agent-contract-runtime
```

隔离维度：

- `principal_id`：本地用户或组织身份；
- `project_id`：逻辑项目；
- `workspace_id`：一次 clone/worktree；
- `agent_kind`：claude-code / codex；
- `native_session_id`：宿主会话；
- `acr_session_id`：跨宿主统一会话标识。

默认禁止跨项目查询原始事件。跨项目共享契约必须显式发布到用户级或组织级 registry，并保留来源。

## 10. 可选模型的角色

模型只用于：

- 将自然语言编译成契约草稿；
- 从事件序列提议失败原因或契约候选；
- 为人类生成解释和摘要；
- 对无法用确定性逻辑处理的低风险策略给出建议分数。

模型不得直接：

- 激活高风险契约；
- 覆盖确定性拒绝；
- 删除或改写历史事件；
- 在缺少验证器时静默修正不可逆操作。

## 11. 记忆晋升管线

事件记录和长期记忆写入必须解耦：

```text
Hook/Event
  -> Evidence Store
  -> Memory Write Gate
  -> Candidate
  -> 去重 / 冲突 / 有效期检查
  -> Active Memory
  -> Context Retrieval
```

热路径只同步保存必要事件和更新会影响当前安全判断的关键状态。普通记忆提取、压缩和候选蒸馏在后台完成，不能阻塞 `PreToolUse`。

完整状态机、优先级和压缩规则见 [跨 Agent 记忆生命周期](08-memory-lifecycle.md)。

## 12. 覆盖完整性

ACR 的事实源还必须描述自己“看漏了什么”。当 Hook 未信任、关闭、崩溃或宿主不兼容时，写入 `coverage_gap`，使受影响的验证和工作状态进入待复核状态。

Native 模式只表示原生集成正在使用，不自动等于事件完整或强制有效。实时探针、人工对账和更强的 Guarded 边界属于后续演进能力，见 [未来功能](14-future-capabilities.md)。
