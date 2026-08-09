# 事件、状态图与上下文模型

## 1. 事实、推断与规则必须分开

ACR 存储三类不同对象：

| 类别 | 示例 | 是否事实源 |
| --- | --- | --- |
| Event | Agent 提议命令、命令输出、人工批准 | 是 |
| Contract | 经审批的约束和修正规则 | 是 |
| Projection | “模块 A 已验证”“方案 B 曾失败” | 否，可从前两者重建 |

不得把模型摘要直接当成事实。摘要必须标记为 `derived`，并引用源事件。

## 2. 事件信封

```json
{
  "schema_version": "acr.event/v1alpha1",
  "event_id": "01J...",
  "project_id": "01J...",
  "workspace_id": "01J...",
  "sequence": 42,
  "occurred_at": "2026-10-19T00:00:00Z",
  "recorded_at": "2026-10-19T00:00:00Z",
  "type": "action.rewritten",
  "actor": {
    "kind": "agent",
    "agent_kind": "claude-code",
    "native_session_id": "...",
    "acr_session_id": "..."
  },
  "causality": {
    "correlation_id": "...",
    "causation_event_id": "...",
    "native_turn_id": "...",
    "native_tool_use_id": "..."
  },
  "payload": {},
  "evidence_refs": [],
  "redaction": {
    "applied": true,
    "rules": []
  },
  "integrity": {
    "previous_hash": "...",
    "payload_hash": "...",
    "event_hash": "..."
  }
}
```

`occurred_at` 来自事件发生时间，`recorded_at` 是 ACR 落库时间。排序以项目内 `sequence` 为准，不依赖可能漂移的系统时间。

## 3. 最小事件集合

### 会话与任务

- `session.started`
- `session.resumed`
- `session.compacted`
- `session.ended`
- `intent.observed`
- `context.delivered`

### 操作

- `action.proposed`
- `action.passed`
- `action.rewritten`
- `repair.planned`
- `repair.started`
- `repair.completed`
- `repair.failed`
- `action.asked`
- `action.denied`
- `action.completed`
- `action.failed`

### 契约

- `contract.drafted`
- `contract.validated`
- `contract.approved`
- `contract.activated`
- `contract.suspended`
- `contract.retired`
- `exception.granted`
- `exception.expired`

### 验证与投影

- `verification.passed`
- `verification.failed`
- `projection.invalidated`
- `candidate.proposed`
- `candidate.rejected`

### 记忆治理

- `memory.candidate_created`
- `memory.activated`
- `memory.confirmed`
- `memory.stale`
- `memory.contested`
- `memory.corrected`
- `memory.superseded`
- `memory.retracted`
- `memory.archived`
- `memory.compacted`

### 覆盖与恢复

- `coverage.gap_detected`
- `coverage.gap_reconciled`
- `runtime.degraded`
- `runtime.mode_change_requested`
- `runtime.mode_changed`
- `reconciliation.started`
- `reconciliation.committed`

### 存量项目初始化

- `bootstrap.started`
- `bootstrap.source_discovered`
- `bootstrap.source_skipped`
- `bootstrap.candidate_extracted`
- `bootstrap.conflict_detected`
- `bootstrap.candidate_approved`
- `bootstrap.candidate_rejected`
- `bootstrap.completed`

## 4. Intent 的准确含义

Hook 能直接观察用户提示和 Agent 的工具提议，但不能读取一个客观存在的“真实意图”。因此：

- `intent.observed` 保存用户提示、任务元数据和来源；
- `intent.inferred` 若未来引入，必须标记模型、置信度和证据；
- 不得用推断意图覆盖用户原文；
- 修正引擎以明确工具调用和契约为主要输入，不能只凭推断意图执行高风险更改。

## 5. 操作与结果关联

每次工具调用使用 `correlation_id` 串联：

```text
intent.observed
  -> action.proposed
      -> action.rewritten (可选)
          -> action.completed | action.failed
              -> verification.passed | verification.failed
```

并行工具调用各有独立 correlation ID，通过 causation ID 保留父子关系。事件写入顺序不代表实际完成顺序。

## 6. 对象存储与脱敏

大输出不直接塞入 SQLite 事件行：

1. 写入前应用脱敏规则。
2. 计算脱敏后内容摘要。
3. 压缩写入内容寻址对象库。
4. 事件只保存摘要、大小、媒体类型和对象引用。

默认永不记录：

- 环境变量全量快照；
- 已识别的 API key、token、密码和私钥；
- 操作系统凭据存储内容；
- 明确标记 `no_record` 的字段。

为保证策略仍可判断，可在内存中检查原值，但持久化时只保存分类结果或不可逆摘要。

## 7. 哈希链

每个项目维护独立链：

```text
event_hash = H(
  canonical_event_without_integrity
  || previous_hash
  || payload_hash
)
```

要求：

- 使用规范 JSON 编码；
- 写入事件与更新项目链头在同一数据库事务；
- 启动和导出时可校验全链；
- 支持周期性签名 checkpoint；
- 明确声明它是篡改检测，不是本地不可篡改保证。

## 8. 项目状态图

### 8.1 节点类型

- `Project`
- `Workspace`
- `Module`
- `Artifact`
- `Decision`
- `Contract`
- `Verification`
- `FailurePattern`
- `ExternalSystem`
- `Event`

### 8.2 边类型

- `CONTAINS`
- `DEPENDS_ON`
- `GOVERNS`
- `DERIVED_FROM`
- `VERIFIED_BY`
- `INVALIDATED_BY`
- `FAILED_IN`
- `SUPERSEDES`
- `TOUCHES`

每个节点和边都包含：

- 来源事件；
- 创建投影器版本；
- 置信类型：`observed` / `verified` / `inferred`；
- `valid_from` / `valid_until`；
- 与当前文件内容相关时的文件摘要。

### 8.3 SQLite 实现

MVP 使用普通关系表：

- `graph_nodes(id, project_id, type, key, attributes, ...)`
- `graph_edges(id, project_id, from_id, type, to_id, attributes, ...)`
- `projection_sources(projection_id, event_id)`

查询深度限制在 1–3 跳，无需图数据库。只有真实性能数据证明关系查询成为瓶颈时才评估专用图存储。

## 9. 状态失效

“测试已经通过”不是永久事实。验证证据至少绑定：

- 项目和工作区；
- 涉及文件/目录摘要；
- 命令及规范化参数；
- 环境指纹；
- 执行时间；
- 退出状态；
- 可配置 TTL。

相关输入发生变化时，投影器写入失效事件，不删除原验证历史。

## 10. 上下文包

上下文包不是聊天摘要，而是带预算和来源的结构化结果：

```json
{
  "schema_version": "acr.context/v1alpha1",
  "hard_contracts": [],
  "relevant_state": [],
  "prior_failures": [],
  "verified_work": [],
  "open_questions": [],
  "evidence_index": [],
  "truncation": {
    "occurred": false,
    "omitted_count": 0
  }
}
```

排序原则：

1. 当前操作适用的硬契约；
2. 当前路径和模块的已验证状态；
3. 会导致重复踩坑的失败证据；
4. 尚未完成或待确认事项；
5. 其他近邻状态。

每项带 `why_relevant` 和 `evidence_refs`。Agent 不需要相信无来源的断言。

## 11. 回放

回放分两种：

- **认知回放**：向新 Agent 展示原始操作、关键输出、错误和结论，不执行副作用。
- **执行回放**：在隔离环境重放动作，用于复现；默认关闭，必须显式授权。

用户提出的“让新 Agent 亲历失败”默认应理解为认知回放。未经授权不能把历史破坏性命令重新执行。

## 12. 记忆不是事件摘要

L0 Event、L1 Memory 和 L2 Contract 具有不同生命周期。事件保存发生过什么；记忆保存哪些证据会改变未来决策；契约保存运行时必须执行的规则。

Memory Write Gate、错误记忆修正、P0–P6 优先级和 Hot/Warm/Cold 压缩策略见 [跨 Agent 记忆生命周期](08-memory-lifecycle.md)。
