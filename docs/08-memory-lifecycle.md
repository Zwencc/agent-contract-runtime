# 跨 Agent 记忆生命周期详细设计

## 1. 先定义什么是“记忆”

ACR 不能把所有聊天和工具输出都叫作记忆。建议把持久信息拆成三层：

| 层 | 内容 | 作用 | 是否直接注入 Agent |
| --- | --- | --- | --- |
| L0 Evidence | 原始事件、工具输入输出、人工操作 | 可追溯证据 | 否，按需查询 |
| L1 Memory | 从证据中提取的有效事实、决策、任务状态、失败模式 | 跨会话连续性 | 按相关性注入 |
| L2 Contract | 已审批、需要执行的约束和修正规则 | 改变或限制操作 | 始终参与适用性计算 |

三层之间只能按显式流程升格：

```text
Evidence -> Memory Candidate -> Active Memory -> Contract Candidate -> Active Contract
```

事件被记录，不代表它值得成为长期记忆；记忆被激活，也不代表它应该成为强制契约。

## 2. 记忆类型

L1 Memory 至少包含以下类型：

| 类型 | 示例 | 默认寿命 |
| --- | --- | --- |
| `user_correction` | “这个目录不能格式化，是我明确要求的” | 直到撤销或升格契约 |
| `decision` | “认证统一采用 OIDC，不再自建密码体系” | 直到被新决策取代 |
| `work_state` | “支付模块重构完成到步骤 3，步骤 4 未做” | 任务完成或取消 |
| `verification` | “输入摘要 X 上测试通过” | 输入变化或 TTL 到期 |
| `failure_pattern` | “方案 A 在版本 B 因错误 C 失败” | 原因条件失效前 |
| `project_fact` | “生成代码来自 schema 目录” | 来源变化前 |
| `preference` | “优先给命令，不自动执行迁移” | 用户撤销或长期未使用 |
| `open_question` | “是否兼容 Windows 尚未决定” | 问题关闭前 |
| `handoff` | 本轮完成、未完成、下一步与风险 | 被后续状态吸收后归档 |

模型生成的推断不能伪装成 `project_fact`，必须使用 `inferred` 置信类型并等待验证。

## 3. ACR 什么时候更新记忆

### 3.1 更新触发器

ACR 在以下时点运行 Memory Write Gate：

1. **用户明确纠正或指示长期约束时**：`UserPromptSubmit` 捕获“记住、以后、始终、不要、之前说错了”等候选，但不只靠关键词判断。
2. **工具产生有状态结果时**：文件修改、测试、构建、迁移、部署、回滚、配置变更和外部系统写入完成后。
3. **验证器得出结论时**：成功与失败都可以更新验证和失败模式。
4. **任务状态变化时**：开始、完成、阻塞、取消或范围调整。
5. **Agent 停止、会话结束或压缩前后**：对本轮事件做一次对账，生成 handoff candidate。
6. **已有记忆的来源发生变化时**：使旧记忆失效、进入争议或要求重验。
7. **人工执行记忆命令时**：添加、确认、纠错、撤销、合并、固定或归档。
8. **重复模式达到阈值时**：提出 failure/contract candidate，但不自动升格为硬契约。

### 3.2 不触发持久记忆的内容

- Agent 的普通解释、计划草稿或寒暄；
- 没有工具证据的“应该已经完成”；
- 一次性命令的完整输出，除非它支撑后续判断；
- 与未来行为无关的中间推理；
- 可以随时从代码低成本重新计算的信息；
- 没有明确项目作用域的模糊结论；
- 含敏感信息且无法安全脱敏的载荷。

这些内容可以留在当前会话或 L0 事件保留策略中，不进入活跃记忆。

### 3.3 同步更新与异步更新

为避免“下一次工具调用仍使用旧状态”，以下变化必须在相关 Hook 返回前同步提交：

- 用户明确撤销或纠正一条当前适用记忆；
- 工具结果使现有 verification 失效；
- 当前任务的完成、失败或阻塞状态；
- 覆盖缺口和运行保证等级变化；
- 会影响当前操作安全判断的状态。

以下工作可以异步执行：

- 相似失败聚类；
- 低风险 project fact 候选提取；
- 记忆压缩和冷热迁移；
- 向量/语义索引；
- 契约候选蒸馏；
- 长期质量统计。

如果异步任务失败，原始事件仍然存在，可稍后重建；不能因此丢失关键纠正或错误地维持旧验证。

## 4. Memory Write Gate

每个候选必须回答七个问题：

1. **Actionability**：它会改变未来 Agent 的操作、验证或检索范围吗？
2. **Evidence**：是否有用户原文、工具结果、文件状态或审批作为证据？
3. **Scope**：它属于哪个用户、项目、工作区、模块或任务？
4. **Stability**：预计多久有效，什么变化会让它失效？
5. **Novelty**：现有记忆是否已经表达了同一事实？
6. **Risk**：忘记它会造成安全问题、返工还是仅有轻微不便？
7. **Sensitivity**：长期保存是否会引入隐私或凭据风险？

写入规则：

- 缺少 Evidence 或 Scope：保持 candidate，不进入 Active。
- 不具备 Actionability：只保留事件，不创建记忆。
- 与现有记忆重复：增加证据引用和 `last_confirmed_at`，不新增副本。
- 与现有记忆冲突：双方进入 `contested`，不能静默覆盖。
- 高风险且来源仅为 Agent 推断：必须人工确认。
- 用户明确纠正：立即成为高权威 candidate；若涉及安全或执行约束，再走契约审批。

## 5. 记忆对象

```yaml
apiVersion: acr.dev/v1alpha1
kind: Memory
metadata:
  id: 01JMEMORYEXAMPLE0000000000
  project_id: 01JPROJECTEXAMPLE0000000000
  type: failure_pattern
  state: active
  created_at: 2026-10-19T00:00:00Z
  last_confirmed_at: 2026-10-19T00:00:00Z
content:
  claim: 方案 A 在 Node 版本 20 下会因错误 C 失败
  normalized_key: failure:module-a:approach-a:node20:error-c
scope:
  modules: [module-a]
  paths: [packages/module-a/**]
  tasks: []
authority:
  source_kind: tool_evidence
  confirmed_by: []
confidence:
  level: verified
  reasons: [command_failed_with_matching_error]
lifecycle:
  valid_from: 2026-10-19T00:00:00Z
  valid_until: null
  invalidation_conditions:
    - runtime.node_major != 20
    - files.matching("packages/module-a/**").digest_changed
priority:
  impact: medium
  recurrence: high
  retrieval_class: P3
evidence_refs:
  - event:01JEVENT...
supersedes: []
superseded_by: null
```

最终 Schema 应保持字段正交，避免用单一“置信度分数”掩盖来源、时效和风险差异。

## 6. 记忆状态机

```text
candidate -> active -> confirmed -> stale -> archived
    │          │          │          ├──> contested -> active | retracted
    │          │          ├──────────> superseded
    │          └─────────────────────> retracted
    └────────────────────────────────> retracted | archived
```

- `candidate`：尚未满足写入门槛或等待人工确认。
- `active`：可参与检索和上下文包。
- `confirmed`：已有新增证据再次确认，可参与正常检索。
- `stale`：可能过期，不再作为确定事实注入。
- `contested`：出现冲突证据，只能向 Agent 说明冲突，不能选边。
- `superseded`：被新版本替代，保留谱系。
- `retracted`：确认错误，不再参与检索。
- `archived`：仍可审计，但退出常规检索。

## 7. 错误记忆怎么处理

### 7.1 不直接删除事实历史

普通错误修正采用追加式事件：

```text
memory.active
  -> memory.contested
  -> memory.retracted 或 memory.superseded
```

旧记忆立即退出上下文检索，但原记录继续存在，以便解释“之前为什么会这样认为”。

只有隐私、凭据或法规要求才执行载荷清除；清除后保留不含原文的 tombstone 和原因。

### 7.2 错误发现来源

- 用户明确说“这条记忆是错的”；
- 新工具证据与旧 claim 冲突；
- 绑定的文件、配置或依赖摘要变化；
- 验证 TTL 到期；
- 两个高权威来源互相冲突；
- 记忆引用的路径、模块或外部系统已不存在；
- `acr audit memory` 人工审计发现问题。

### 7.3 人工命令草案

```text
acr memory show <id> --with-evidence
acr memory correct <id> --claim "正确内容" --reason "..."
acr memory retract <id> --reason "..."
acr memory confirm <id>
acr memory pin <id>
acr memory unpin <id>
acr memory merge <id> <id>
acr memory audit --stale --contested --low-confidence
```

`correct` 不修改原对象，而是创建新版本并建立 `supersedes` 关系。

## 8. 必要记忆的判断

一条信息只有满足以下至少一项才是必要记忆：

- 忘记后可能违反安全、合规或用户硬边界；
- 忘记后会重复一次已有证据证明无效的高成本工作；
- 它描述尚未完成、必须交接的工作；
- 它是无法从当前仓库低成本恢复的设计决策及理由；
- 它是与具体输入版本绑定的验证证据；
- 它改变某类操作应走的工具、路径、顺序或审批方式；
- 用户明确要求跨会话保留。

下列通常不属于必要记忆：

- 可以直接从代码读取且读取成本很低的静态事实；
- 已经完成且不会影响后续工作的细枝末节；
- 未验证的 Agent 猜测；
- 没有未来动作价值的长篇聊天摘要；
- 多条相同事件的重复文本。

核心原则是“保存未来决策差异”，不是“保存过去所有文本”。

## 9. 记忆优先级

### 9.1 检索优先级

优先级不能只用一个数字。ACR 按以下顺序做词典序排序：

1. **Authority**：用户明确纠正 / 已审批契约 > 确定性工具证据 > 仓库观察 > Agent 推断 > 模型摘要。
2. **Safety impact**：可能造成不可逆或安全影响的内容优先。
3. **Task relevance**：与当前路径、模块、工具、操作和目标直接相关的优先。
4. **Lifecycle**：active/pinned 优先；stale、contested 只作为警告；retracted 不进入正常检索。
5. **Confidence**：verified > corroborated > observed > inferred。
6. **Recurrence value**：能阻止重复踩坑或重复分析的优先。
7. **Freshness**：在其他条件相同后才比较时间。
8. **Context cost**：信息价值相同时，短且结构化的优先。

### 9.2 默认优先级类别

| 类别 | 内容 | 默认处理 |
| --- | --- | --- |
| P0 | 安全边界和人工硬约束 | 必须提供，不可被摘要省略 |
| P1 | 当前任务约束、未完成工作和阻塞项 | 必须提供或明确报告无法提供 |
| P2 | 已验证的架构决策 | 高优先注入 |
| P3 | 近期失败模式和验证证据 | 按相关性注入 |
| P4 | 稳定模块知识和普通项目事实 | 有预算时注入 |
| P5 | 普通工作历史 | 可压缩或仅保留索引 |
| P6 | 低价值临时信息 | 不主动注入，按需查询 |

时间新不等于优先级高。一条昨天的闲聊不能覆盖一年前仍有效的硬契约。

## 10. 记忆压缩

### 10.1 三温层存储

- **Hot**：当前适用契约、当前任务、近期阻塞和高相关记忆。
- **Warm**：活跃但当前不相关的结构化 Memory Card。
- **Cold**：原始事件、大对象和已归档记忆。

压缩是 Hot/Warm/Cold 之间的迁移和去重，不等于删除证据。

### 10.2 压缩流水线

1. 精确去重：规范 key、内容摘要和事件关联。
2. 结构化合并：相同 claim 增加证据，不复制文本。
3. 状态折叠：连续任务事件折叠为当前 work state，同时保留事件链。
4. 失败聚类：同一根因的多次失败合并为一个 pattern，记录发生次数和样例。
5. 生成 Memory Card：短 claim、作用域、有效期、证据索引。
6. 冷归档：低相关且长期未命中的卡片退出常规检索。
7. 重新索引：更新模块、路径和操作类型索引。

### 10.3 禁止“摘要套摘要”

压缩后的 Memory Card 必须直接引用原始事件。后续重新压缩时从当前卡片的源事件重建，不把旧摘要当成唯一输入，避免语义逐代漂移。

### 10.4 压缩触发条件

- 会话正常结束；
- 上下文预算达到阈值；
- 同一 normalized key 候选过多；
- 项目事件量达到分段阈值；
- 后台维护窗口；
- 用户显式执行 `acr memory compact`。

压缩不应在 PreToolUse 热路径运行。

### 10.5 上下文预算建议

默认按比例预留，但以最低保证优先：

- 30%：P0/P1 契约和纠正；不足时扩张，绝不截断关键语义；
- 25%：P2 当前任务与阻塞；
- 25%：P3 当前模块验证、决策和失败；
- 10%：P4/P5 其他高相关信息；
- 10%：证据索引、遗漏说明和查询提示。

若 P0/P1 已耗尽预算，应注入紧凑索引并要求 Agent 查询，不能静默漏掉约束。

## 11. 记忆失效与衰减

不同类型采用不同规则：

- Contract：不按时间自动衰减，只能被暂停、撤销或新版本取代。
- User correction：保持有效，直到用户撤销或明确缩小作用域。
- Verification：输入摘要变化或 TTL 到期立即失效。
- Work state：任务完成、取消或被新状态取代后归档。
- Decision：相关模块/接口发生结构变化时标记待复核，不自动删除。
- Failure pattern：触发条件不再成立时归档；新环境再次命中可重新激活。
- Preference：长期未使用可降低检索权重，但不能静默当作相反偏好。
- Inference：短 TTL，未被证据确认就退出活跃层。

## 12. 跨 Agent 一致性

Claude Code 与 Codex 读取同一个 Active Memory Registry。Adapter 不能维护各自独立的记忆副本。

每次上下文交付记录：

- 哪些记忆被选择；
- 为什么相关；
- 哪些因预算被省略；
- 交付给哪个 Agent/会话；
- 当时的记忆版本。

这样可以区分“ACR 没有这条记忆”“ACR 有但检索没选中”“已交付但 Agent 未遵守”三种完全不同的问题。

## 13. 记忆质量指标

- `capture_precision`：升格为 Active 的记忆中有多少真正有用。
- `retrieval_precision`：注入内容中有多少与当前任务相关。
- `critical_recall`：适用 P0/P1 是否全部交付。
- `stale_injection_rate`：注入后被证明已过期的比例。
- `duplicate_rate`：同一 claim 的重复卡片比例。
- `correction_latency`：用户纠错到旧记忆退出检索的时间。
- `compression_drift_rate`：压缩卡片与源证据语义不一致的比例。
- `avoided_rework`：因历史证据避免的重复分析或失败次数。

正式版本发布前必须为这些指标建立可重复的离线评测集，不能只凭演示效果判断记忆质量。

## 14. 当前实现（v0.1 alpha）

当前已实现独立 SQLite Memory Store，避免把可变检索状态混入追加事件表：

- 新内容只能以 `candidate` 创建，不能在捕获时绕过人工激活；
- `active`、`confirmed` 进入正常检索，`contested` 仅在审计查询显式要求时返回，其余非活跃状态立即退出正常检索；
- 每次状态变化写入不可变且连续的 revision，错误内容通过 `correct` 原子创建 active 替代项并将旧项标记 `superseded`；
- `retract` 不删除历史，但会让错误记忆立即退出跨 Agent 上下文；
- P0-P6 按优先级稳定检索；过期项不返回，P2-P6 可批量归档，P0/P1 不自动归档；
- 项目、工作区和类型均可隔离检索；SQLite 触发器保护 revision 历史不被更新或删除；
- `acr memory add/list/show/history/activate/confirm/stale/contest/correct/retract/archive` 提供人工闭环；
- Hook 安装会把共享 memory DB 传给两端 SessionStart，上下文包只注入 active/confirmed 项。

当前尚未实现自动 Write Gate、与来源变化联动的精确 stale 标记、语义去重/压缩、路径相关性排序和“每次交付哪些记忆”的独立审计事件。因此本阶段优先保证生命周期正确和人工可修正，不把自动抽取的召回率包装成已解决。
