# 中途接入现有项目：记忆系统初始化设计

## 1. 结论

ACR 中途安装时应主动发现项目已有的 Agent 指令、记忆、契约、决策和工作状态。但初始化分为两部分：

1. **确定性 Bootstrap**：不依赖 LLM，负责发现、读取、解析、分层、计算摘要和保留来源。
2. **LLM-assisted Bootstrap**：可选，负责从非结构化文本提取候选记忆、发现冲突和建议作用域。

LLM 能显著提高旧项目初始化质量，但不能成为运行 ACR 的硬依赖，也不能直接激活记忆或契约。

## 2. 为什么不能只调用一次 LLM

把整个项目文档直接交给模型总结会产生几个问题：

- 丢失不同文件的真实优先级和作用域；
- 把计划、历史描述和当前事实混在一起；
- 忽略 Agent 特有的加载规则；
- 将恶意仓库文本当成指令，形成 prompt injection；
- 无法可靠增量更新，只能反复全量总结；
- 输出缺少精确原文引用，错误后难以纠正；
- 可能把过期内容直接变成活跃契约。

因此，LLM 只能处理经过确定性发现和切片的“候选资料包”。

## 3. Bootstrap 数据源

### 3.1 Agent 指令与本地记忆

首批支持：

| 来源 | 典型位置 | 初始权威类型 |
| --- | --- | --- |
| Codex 项目指令 | 根目录及子目录 `AGENTS.md`、`AGENTS.override.md` | repository_instruction |
| Codex 用户指令 | Codex 配置目录中的 `AGENTS.md` | user_instruction |
| Claude 项目指令 | `CLAUDE.md`、`.claude/CLAUDE.md`、`CLAUDE.local.md` | repository/user_local_instruction |
| Claude 路径规则 | `.claude/rules/*.md` | path_scoped_instruction |
| Claude auto-memory | 对应项目的 Claude memory 目录 | agent_generated_memory |
| ACR 原生契约 | `.acr/contracts/*` | structured_contract |
| ACR 导出/交接 | `.acr/ACR_CONTEXT.md`、ACR export | acr_generated，不重复导入 |

Codex 会从配置目录和仓库根到当前目录发现并合并 `AGENTS.md`，更靠近工作目录的内容后应用。[OpenAI 官方 Codex Prompting Guide](https://developers.openai.com/cookbook/examples/gpt-5/codex_prompting_guide)

Claude Code 会按自身规则加载 `CLAUDE.md`、`CLAUDE.local.md`、`.claude/rules/`，在特定条件下也会读取 `AGENTS.md`；Claude auto-memory 是每仓库的本地记忆目录。ACR 不能假设 Claude 与 Codex 看见的是同一组文件。[Claude Code Memory 官方文档](https://code.claude.com/docs/en/memory)

### 3.2 项目制度与决策文件

候选位置包括：

- `README*`
- `CONTRIBUTING*`
- `SECURITY*`
- `ARCHITECTURE*`
- `docs/adr/`、`adr/`、`decisions/`
- `PLANS.md`、`GOALS.md`、`ROADMAP*`
- 项目自定义 build log、handoff、context 目录
- CI 工作流、测试配置、构建清单和部署说明
- 明确配置的内部规则目录

这些文件默认只是候选来源。README 中的示例命令不能自动变成硬契约，ROADMAP 中的计划也不能自动变成“已经完成”。

### 3.3 结构化事实

ACR 可以无需 LLM 读取：

- 语言和包管理器清单；
- 测试、构建和 lint 脚本名称；
- CI 中实际运行的验证命令；
- 工作区/monorepo 结构；
- Schema 明确的策略文件；
- 文件和目录摘要；
- 已存在的 ACR export。

结构化读取只生成“观察事实”，不自动推断团队意图。

### 3.4 系统外部来源

默认不扫描整个主目录或其他项目。用户可显式授权 Adapter 读取当前项目对应的 Agent 本地记忆目录，也可以导入：

- 导出的 Agent 会话；
- issue/任务系统；
- CI 历史；
- 团队 Wiki；
- 其他记忆系统的标准导出。

每个外部 Connector 必须独立授权、限定项目和保留 provenance。

## 4. Agent 有效配置解析

ACR 不能仅按文件名搜索，还要模拟各 Agent 的有效加载视图。

### 4.1 Codex 视图

记录：

- 配置目录指令；
- 项目根到工作目录的层级；
- override/fallback 文件；
- 每段指令适用的目录；
- 文件内容摘要和发现顺序。

结果是 `effective_sources(agent=codex, cwd=...)`，而不是把所有 AGENTS 文件拼成一条无来源文本。

### 4.2 Claude Code 视图

记录：

- managed、user、project、local 各层来源；
- `CLAUDE.md` 与 `AGENTS.md` 的实际选择模式；
- `@path` import 谱系；
- `.claude/rules/` 的路径条件；
- 子目录按需加载范围；
- auto-memory 的项目映射和内容摘要。

Claude Code 官方文档说明，项目中同时存在 `CLAUDE.md` 和 `AGENTS.md` 时，默认读取行为并非简单的“两者全部加载”，且可以由 Project instructions 设置改变。因此 ACR 必须探测实际配置，不能仅凭文件存在推断。[Claude Code Memory 官方文档](https://code.claude.com/docs/en/memory)

### 4.3 跨 Agent 兼容视图

初始化结果分为：

- `shared_consistent`：两个 Agent 的来源表达同一规则；
- `agent_specific`：只对某个 Agent 或工具成立；
- `conflicting`：来源互相矛盾；
- `unloaded_source`：文件存在，但目标 Agent 默认不会加载；
- `unknown`：无法确认当前宿主版本或设置下是否生效。

冲突不能靠来源数量投票。必须展示原文、作用域和宿主加载顺序，由人决定 ACR 的规范版本。

## 5. 初始化流水线

```text
Project Identification
  -> Source Discovery
  -> Agent Effective-View Resolution
  -> Deterministic Parsing
  -> Source Snapshot
  -> Optional LLM Extraction
  -> Candidate Deduplication
  -> Conflict & Staleness Analysis
  -> Read-only Verification
  -> Human Review
  -> Activate Memory
  -> Separately Approve Contracts
  -> Baseline Checkpoint
```

### Step 1：识别项目

- 确定项目根和工作区根；
- 创建或确认 `project_id`；
- 检查是否已经存在 ACR 数据，避免重复初始化；
- 记录本次扫描边界，不越权扫描父目录或其他仓库。

### Step 2：发现来源

- 使用 Adapter 的来源发现规则；
- 应用 ignore、文件大小和二进制限制；
- 解析 import，但限制深度、循环和工作区逃逸；
- 为每个来源计算摘要、媒体类型和权限分类。

### Step 3：确定性解析

- 结构化契约按 Schema 解析；
- Markdown 保留标题层级、列表、代码块和行号；
- CI/package 配置提取结构化命令；
- 明确区分规范、计划、事实、历史、示例和未完成项；
- ACR 自己生成的内容用 provenance 标记，防止回灌循环。

### Step 4：可选 LLM 抽取

模型只接收小批量、带来源边界的文本块，输出严格 JSON：

```json
{
  "candidates": [
    {
      "type": "user_correction|decision|project_fact|work_state|failure_pattern|contract_candidate",
      "claim": "...",
      "scope": {},
      "source_spans": [
        {"source_id": "...", "start_line": 10, "end_line": 12}
      ],
      "temporal_status": "current|historical|planned|unknown",
      "confidence": "high|medium|low",
      "ambiguities": [],
      "suggested_invalidation_conditions": []
    }
  ]
}
```

没有 `source_spans` 的候选直接丢弃。模型不得调用工具、读取额外文件、修改项目或激活结果。

### Step 5：去重与冲突

- 同一 normalized key 合并证据；
- 语义相似但作用域不同的规则保持独立；
- 计划与已完成状态不得合并；
- Agent auto-memory 与人工指令冲突时，人工指令权威更高；
- 两个明确人工来源冲突时进入 contested，等待选择；
- 已过期版本文档只保留为历史证据。

### Step 6：只读验证

验证候选是否与当前项目一致：

- 声称的路径、脚本和配置是否存在；
- build/test 命令是否在清单或 CI 中出现；
- 决策所指模块是否仍存在；
- work state 是否有产物或验证证据；
- 禁止规则能否映射到实际工具和资源。

默认不运行测试、构建、数据库或外部请求。需要执行验证时列出命令并单独授权。

### Step 7：人工 Review

Review 按风险分组：

1. 冲突和硬约束候选；
2. 用户/组织级来源；
3. 当前任务和未完成工作；
4. 决策、验证和失败模式；
5. 普通项目事实；
6. 低置信推断。

用户可以逐项或批量：接受为记忆、改写后接受、提升为契约候选、仅索引来源、归档或拒绝。

### Step 8：建立 Baseline

完成后写入：

- source snapshot；
- active memory registry；
- contested/unknown 列表；
- 已批准契约；
- 工作区摘要；
- bootstrap coverage；
- 模型和提示版本（若使用 LLM）；
- 人工审批事件。

## 6. 是否必须接入 LLM

### 6.1 无 LLM 模式必须可用

无 LLM 时 ACR 仍能：

- 发现并索引所有受支持来源；
- 计算 Claude/Codex 有效来源视图；
- 导入结构化契约；
- 提取 package/CI 中的结构化命令；
- 检测内容摘要变化和部分精确冲突；
- 生成可人工审阅的来源清单；
- 建立证据 baseline。

缺点是非结构化决策、失败经验和作用域需要更多人工标注。

### 6.2 LLM-assisted 模式推荐但可选

LLM 适合：

- 将散落自然语言分类成候选记忆类型；
- 发现近义重复和潜在冲突；
- 区分“计划做”“已经做”“禁止做”；
- 建议作用域、失效条件和验证方式；
- 生成供人审阅的简短解释。

LLM 不适合：

- 决定来源优先级；
- 自动解决冲突；
- 直接激活硬契约；
- 判断真实命令是否已经成功执行；
- 在缺少证据时填补历史；
- 读取未经授权的用户级或其他项目记忆。

## 7. 模型接入安全

仓库文本和旧记忆都视为不可信数据。LLM 抽取器必须：

- 使用 data-only 系统提示；
- 不提供 shell、文件写入、网络和 MCP 工具；
- 使用严格结构化输出；
- 每个 claim 强制引用原文行；
- 对“忽略前文、执行命令”等内容只当普通文本；
- 发送远程模型前先脱敏；
- 默认不上传用户级全局指令和 Agent 私有记忆；
- 支持本地模型或完全离线模式；
- 保存 provider、model、prompt version 和输入摘要以便重现。

## 8. 初始化模式

```text
acr init --mode quick
acr init --mode assisted
acr init --mode deep
```

### Quick

- 零 LLM；
- 扫描 Agent 指令和结构化契约；
- 建立来源索引与 baseline；
- 目标耗时秒级。

### Assisted

- 扫描常见项目文档；
- 使用配置的 LLM 生成候选；
- 必须人工 review；
- 推荐默认交互模式。

### Deep

- 包含 ADR、计划、build log、允许的 Agent 本地记忆和可选外部来源；
- 进行冲突分析和只读验证；
- 适合长期、复杂或中途接入的大型项目；
- 必须先显示扫描范围、隐私范围和预计成本。

## 9. CLI 草案

```text
acr init --mode quick|assisted|deep --dry-run
acr bootstrap sources
acr bootstrap sources --effective-for claude --cwd <path>
acr bootstrap sources --effective-for codex --cwd <path>
acr bootstrap scan
acr bootstrap extract --provider <provider>
acr bootstrap conflicts
acr bootstrap review
acr bootstrap apply
acr bootstrap report
```

`--dry-run` 输出将读取的项目内路径、可能读取的项目外路径、是否调用远程模型、预计发送的数据类型和不会执行的操作。

## 10. 增量重扫

初始化不是一次性全量导入：

1. 每个来源保存内容摘要和解析器版本。
2. 文件未变化则不重新抽取。
3. 文件变化只重新处理受影响片段和派生候选。
4. 来源删除不会立即删除记忆，而是触发 stale/contested 检查。
5. 用户接受过的修正不会被后续 LLM 重扫覆盖。
6. Agent 加载配置变化会重算 effective view。
7. ACR 自身导出不重新摄入，防止记忆回声。

## 11. 中途安装的完整性声明

Bootstrap 结束必须生成报告：

```text
Sources discovered: 18
Sources parsed: 17
Sources skipped: 1 (too large)
Agent-local sources authorized: Claude yes, Codex no
LLM extraction: enabled, 9 redacted chunks
Active memories: 24
Contract candidates: 7, active contracts: 0
Conflicts: 3
Unknown/stale candidates: 5
Historical coverage before ACR: partial, document-derived only
Runtime coverage after ACR: pending live hook verification
```

最重要的一句是：**ACR 安装前的历史覆盖只能是“文档推断和现状观察”，不能冒充完整事件历史。**

## 12. 验收标准

- 同一项目在 Quick 模式下不使用 LLM 也能完成初始化。
- 能正确列出 Claude Code 和 Codex 实际可能读取的不同来源集合。
- 同一规则出现在多个文件时不会产生无界重复记忆。
- 冲突的 AGENTS.md/CLAUDE.md 不会被自动合并成确定规则。
- LLM 候选均带精确来源位置，去掉来源后无法激活。
- 项目文本中的 prompt injection 不会触发工具或扩大扫描范围。
- 第二次扫描只处理变化来源。
- ACR 安装前的未知操作明确显示为历史 coverage gap。
- 用户可以在应用前预览所有将激活的记忆和契约候选。
- 初始化结束后必须再通过双 Agent live hook probe，才能声明运行时已接管。

## 13. 当前实现（v0.1 alpha）

当前 Quick 路径已经实现以下确定性能力：

- `acr init` 在写入 ACR 项目身份前扫描项目内受支持来源，并将 baseline 原子写入 `.acr/state/bootstrap-sources.json`；
- `acr bootstrap sources` 可独立输出来源清单、来源类型、Agent 适用范围、权威分、SHA-256、行数及 indexed/skipped/invalid 状态；
- 扫描覆盖根目录和子目录的 `AGENTS.md`、`AGENTS.override.md`、`CLAUDE.md`、`CLAUDE.local.md`，以及 `.claude/rules/`、`.acr/contracts/`、ADR 和常见项目文档；
- 不跟随符号链接，不越过显式项目根，并限制单文件大小、总字节数、来源数、遍历目录数和深度；
- 结构化契约在扫描时执行严格解析，但无效契约只进入 invalid 报告，不会激活；
- SessionStart 对来源摘要做增量比较。发生新增、修改或删除时，先追加 `source.snapshot_changed` 事件，再原子推进快照；
- 来源变化是“重新评估记忆”的触发证据，不直接修改 active memory 或 active contract。

尚未实现的 Bootstrap 能力包括 Agent 用户级记忆授权导入、有效加载视图完整模拟、Markdown 精确分块、LLM 候选抽取、冲突审阅和候选应用流程。这些缺口完成前，Quick baseline 只能证明“发现过哪些项目来源及其当时摘要”，不能证明其中自然语言已被理解或执行。
