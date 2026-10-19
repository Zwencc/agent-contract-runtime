# 让 ACR 真正干活：产品闭环与工程指标

## 1. “玻璃大炮”的判定

ACR 即使架构漂亮，只要出现以下任一情况，就仍是玻璃大炮：

- 只能展示拦截，不能把操作修正后做完；
- 记了很多内容，却找不到当前真正需要的三条；
- 误报太多，用户最终关闭 Hook；
- 守护进程一挂，安全保证静默消失；
- 只在演示仓库工作，真实 monorepo、并发 Agent 或长会话就失效；
- 生成“项目状态已完成”，却拿不出工具证据；
- 数据越来越多，只能清库重来；
- 宿主升级后不兼容，用户无法自行诊断和恢复。

因此，ACR 的成功指标不是“存了多少记忆”或“拦截了多少调用”，而是减少多少重复劳动和风险，同时增加多少摩擦。

## 2. 五个必须跑通的工作闭环

### 2.1 安全修正闭环

```text
Agent 提议操作
-> ACR 识别适用契约
-> 自动修正安全参数
-> 执行修正后操作
-> 后置验证
-> 向 Agent 返回真实结果
-> 记录证据
```

验收示例：Agent 写错生成目录，ACR 改到源目录，操作成功，Agent 能继续下一步，而不是停在错误提示上。

### 2.2 前置工作闭环

```text
Agent 提议部署
-> ACR 发现测试证据缺失/失效
-> Guarded 事务运行正确范围测试
-> 测试通过
-> 恢复原部署操作
-> 验证部署结果
```

关键点是保留原始操作，而不是把部署替换成测试后等待 Agent 猜下一步。

### 2.3 跨 Agent 交接闭环

```text
Claude Code 完成部分工作
-> ACR 更新 work_state 和验证证据
-> Codex 启动
-> 收到必要契约、已完成项、未完成项和失败证据
-> 从正确位置继续
```

验收不能只看 Codex “读到了摘要”，而要看它是否避免重新分析和重复执行。

### 2.4 错误记忆纠正闭环

```text
用户指出记忆错误
-> 旧记忆立即退出正常检索
-> 创建纠正版本和来源事件
-> 受影响投影重算
-> 下一 Agent 只收到新结论，并可查看历史原因
```

### 2.5 Hook 故障恢复闭环

```text
Hook 未加载
-> ACR 检测 coverage gap
-> 明示保证等级下降
-> 用户修复 Hook 或执行 reconcile
-> 高风险会话切换 Guarded
-> 重验受影响记忆
```

## 3. 默认体验

一个新项目的正常流程应控制在：

```text
acr init
acr install-hooks --agent all --dry-run
acr doctor --agent all --live
acr contract add ...
```

之后用户日常不需要手动启动多个服务。`acr` 可按需启动用户级守护进程，或在不适合常驻服务时使用直接模式。

日常默认行为：

- PASS 静默完成；
- REWRITE 只显示一行可追踪说明，不弹窗；
- ASK 只用于真实歧义；
- DENY 给出可执行的下一步；
- 记忆候选批量审阅，不在每次工具调用后打扰；
- 故障和保证等级下降必须明显显示。

## 4. 真实任务包

开发期间维护公开的 scenario pack，至少覆盖：

1. monorepo 中只运行受影响包测试；
2. 禁止修改生成目录并改写到源文件；
3. 禁止生产数据库写入；
4. 部署前补齐已失效的测试证据；
5. Claude Code 失败后 Codex 接手且不重复方案；
6. Codex 完成一半后 Claude Code 延续 work state；
7. 用户纠正错误记忆并立即生效；
8. Hook 未信任、守护进程离线和 Schema 升级；
9. 两个 Agent 并行修改同一资源；
10. 长期项目中十万级事件的压缩和检索。

每个场景必须给定初始状态、用户目标、契约、预期修正、允许副作用和最终断言。不能只检查 CLI 返回码。

## 5. SLO 与产品指标

### 5.1 热路径

- PASS 决策 P95 < 50 ms，P99 < 100 ms。
- 本地 REWRITE P95 < 100 ms。
- 热路径默认零网络调用、零模型调用。
- 守护进程重启后不丢已提交事件。

### 5.2 正确性

- P0/P1 适用契约和记忆的 critical recall = 100%。
- 自动改写必须通过改写后二次策略评估。
- 不可逆操作不得仅凭模型评分自动修正。
- work state 的 `completed` 必须有验证证据或明确人工确认。

### 5.3 摩擦

- PASS 不产生交互。
- 同一已知冲突不反复询问。
- 候选记忆和契约支持批量审阅。
- 每次 ASK/DENY 必须提供具体原因、命中规则和可行下一步。
- 记录 `disable_reason`，把用户关闭 ACR 视为最高优先级产品缺陷信号。

### 5.4 实际收益

- 避免的重复分析时间；
- 避免的重复失败次数；
- 自动完成且验证通过的修正数；
- 跨 Agent 交接后首次有效动作耗时；
- 用户手工修正 ACR 决策的频率；
- 因 ACR 导致的误操作和漏拦截。

## 6. 失败必须可操作

每个错误包含：

- 发生了什么；
- 原操作是否已经执行；
- ACR 当前知道和不知道什么；
- 当前保证等级；
- 受影响记忆或契约；
- 一条首选恢复命令；
- 查看详细证据的命令。

禁止只返回“policy failed”“memory unavailable”或内部堆栈。

## 7. 数据规模与性能退化

- 事件按项目和时间分段，索引不扫描大对象。
- 上下文检索先按 scope/path/tool 缩小集合，再做可选语义排序。
- 原始对象进入 Cold 层，不影响热路径。
- 投影可增量更新，也可离线全量重建。
- 压缩和候选蒸馏在后台运行，不能阻塞 PreToolUse。
- 达到容量阈值时提示归档，不自动删除 P0/P1 或未完成任务。

性能评测至少覆盖 1 万、10 万、100 万事件，公开数据库大小、写入延迟、检索延迟和压缩耗时。

## 8. 反过度设计门槛

新增基础设施必须回答：

1. 它解决了哪个已复现用户问题？
2. SQLite/结构化规则为什么不够？
3. 如何离线运行和迁移？
4. 失败时是否影响工具热路径？
5. 如何通过 scenario pack 验证收益？

在真实指标证明需要前，不引入微服务、消息队列、图数据库、独立向量数据库或多模型编排。

## 9. Dogfood 机制

ACR 项目自身必须使用 ACR：

- 本项目契约作为公开样例；
- Claude Code 与 Codex 轮换承担真实开发任务；
- 每次重复分析、误修正、漏记忆和 Hook 故障进入 issue；
- 每个版本发布报告 avoided rework、误修正、打扰率和 coverage gap；
- 若团队频繁绕开 ACR 才能工作，暂停增加功能，优先修复可用性。

## 10. 最低“能干活”标准

在宣称 ACR 可日常使用前，它至少要做到：

- 安装和诊断不超过 5 分钟；
- 两个 Agent 都能自动修正一个真实文件操作和一个结构化工具调用；
- 能完成一个多步前置修复事务；
- 能跨 Agent 延续一个未完成任务；
- 能立即撤销一条错误记忆；
- 能在 Hook 失效后发现缺口并恢复；
- 能在十万事件项目中维持可接受延迟；
- 用户可以完全导出并卸载，不破坏原项目。

不满足这些条件时，应称为实验性预览，而不是生产级跨 Agent 记忆系统。

## 11. 当前 Guarded Repair 实现（v0.1 alpha）

当前已经实现可单独运行的最小多步修复事务：

- 计划使用严格 JSON Schema 版本、计划 ID、幂等键、资源键和有序步骤；
- 每一步使用 argv 数组直接启动进程，ACR 不隐式拼接 shell 命令；cwd 必须位于项目根内；
- 每步必须声明 1–600 秒超时，可声明允许的退出码；默认只接受 0；
- `acr repair run --plan ...` 默认只验证并显示计划摘要，只有显式 `--apply` 才执行；
- 执行前同时获取幂等键锁和全部资源锁，防止同一进程域内的并行冲突；
- 写入 `repair.started`、`repair.step_started`、`repair.step_completed`/`repair.failed` 和 `repair.completed`；
- stdout/stderr 有大小上限并在写入事件前脱敏；
- 同一幂等键完成或失败后不会重复执行；发现只有 started/step_started 的中断事务时返回 recovery required，不猜测步骤是否成功。

在此基础上，当前还实现了：

- **中断恢复**：按事件折叠出每一步的状态（未开始、执行中/结果未知、已完成、已失败）。没有"结果未知"步骤的中断事务会从断点续跑并写入 `repair.resumed`；计划内容与开始时不一致时拒绝续跑。存在"结果未知"的步骤时，由人用 `acr repair resolve --step <id> --outcome succeeded|failed --reason ... --apply` 明确判定（写入 `repair.step_resolved`），ACR 不猜测。`acr repair status` 只读显示各步骤状态。
- **陈旧锁恢复**：锁文件记录持有进程 PID，持有进程已退出时自动清理；无法判断（旧格式锁、Windows）一律视为仍被持有。
- **补偿动作**：步骤可声明 `compensation`（同样的 argv、cwd、超时规则）。后续步骤失败或被人工判定失败时，按逆序补偿已完成且声明了补偿的步骤，每步写入 `repair.compensation_*`；任一补偿失败即停止后续补偿。
- **验证步骤**：步骤可声明 `verification.inputs`（项目相对 glob，空表示整个工作区）。执行前后各计算一次输入指纹，结果写入 `verification.passed/failed`；输入在执行中被改动则标记为 unstable。只由验证步骤组成的计划可声明 `repeatable`，完成或失败后可用同一幂等键重跑。
- **前置证据**：契约可声明 `verifiers.evidence`。命中时若要求的验证在当前输入上全部有效，契约视为已满足；否则按契约的处置方式 DENY/ASK，并在原因中给出缺失项和补救命令。Agent 运行补救计划后重试原操作即可通过，原操作始终由 Agent 保留。
- 修复事务的全部事件以幂等键为 `correlation_id`，可用 `acr replay --correlation <key>` 回放。
- **跨 Agent 路径契约**：条件和 `replace_path` 可以使用 `paths` 字段，它是动作涉及文件的项目相对路径（已规范化，`..` 已消解，项目外路径保留 `../` 前缀，不会命中项目 glob）。glob 在任一路径命中时成立，支持 `**`。对 `paths` 的 `replace_path` 会改写对应工具的路径参数：Claude 的 `file_path`/`notebook_path`、Codex `apply_patch` 的文件头（相对其工作目录）、Guarded 工具的 `file_path`，并同步更新动作的 resources 后再复核。因此一条契约即可同时约束 Claude Code、Codex 和 Guarded 工具（开发计划场景 A，见 `cmd/acr/scenario_test.go`）。Hook 和 Guarded 动作都携带项目根 `workspace.root`。
- **无人值守时 ASK 等于 DENY**：宿主权限模式为 `bypassPermissions` 或 `dontAsk` 时没有人会回答确认提示，需要确认的决策直接拒绝，原因中保留契约给出的缺失信息（场景 D）。Codex Hook 本来就不支持 ask，Guarded 模式同样拒绝。经 `acr launch` 以 `claude -p/--print` 或 `codex exec` 启动时，ACR 设置 `ACR_UNATTENDED=1`，Hook 同样把 ASK 改为 DENY；直接运行宿主（不经 `acr launch`）的非交互会话无法从 Hook 输入识别，仍按 ask 交给宿主处理。
- 经 `acr launch` 启动的会话，Hook、Guarded 工具和启动/结束事件都在 `acr_session_id` 记录启动 ID（即 `runtime.launch_started` 的事件 ID）。`acr replay --session` 接受宿主会话 ID 或启动 ID，并带出同一次启动的另一侧事件。该字段只用于关联回放，环境变量可被伪造，不作为授权依据。
- **记忆与规则由用户口头指令更新**：实际使用中，记忆和规则几乎都是用户在对话里让 Agent 记下或修改的，人工很少逐条审核。因此：
  - Agent 通过 ACR 的 MCP 工具执行：`remember`、`confirm_memory`、`correct_memory`、`forget_memory`、`list_memories`、`add_rule`、`retire_rule`、`list_rules`、`authorize`。Native 模式由 `acr install-hooks` 注册（只含这些工具：Claude 写 `.mcp.json`，首次需要在 Claude Code 里批准一次；Codex 写 `.codex/config.toml`），Guarded 模式与执行工具一起提供。
  - 每次改动都要带用户原话（`quote`）。ACR 只在用户自己发出的提示（`intent.observed`）里查找这段话，文件内容、工具输出和 Claude 的后台任务通知（记为 `host.notification`）都不算；找到后立即生效，并以那条提示为证据记录。Agent 自己决定记的内容只进候选，用户在对话里同意后用 `confirm_memory` 生效。
  - 生效中的契约以事件链为准（`contract.activated` / `contract.retired`，见 `internal/registry`）。项目第一次通过 ACR 激活契约时，会把目录里已生效的契约一并导入；此后目录只是镜像，用其他方式改目录不影响执行，`acr contract list` 和 `acr doctor` 会列出不一致的文件。人在终端用 `acr contract activate|retire|list`；Agent 通过 Bash 调这两个命令、或直接写 `.acr/`，都会被内置契约拒绝。
  - "除非我主动让你改"：契约声明 `authorization: {"user_in_turn": true, "mentions": [...]}`。被拒绝时原因里会说明怎么申请；用户在当轮消息里明确允许后，Agent 调 `authorize` 并引用原话，ACR 核对这段话出自该会话最新一条用户提示且提到了 `mentions` 中的对象，记录 `authorization.granted`。放行只对这条规则、这个会话有效，用户下一条消息一到就失效。
  - 其他正在运行的会话在下一次提示或工具调用时收到 `[new]`、`[new rule]`、`[rule updated]`、`[rule removed]` 通知，只通知一次；发起改动的会话不重复收到。`acr memory review` 按时间列出最近的记忆与规则变化、对应的用户原话和待确认的候选。
- **守护进程**：`acr daemon run|start|stop|status`。每个用户一个进程，监听仅本人可访问目录（`$XDG_RUNTIME_DIR/acr/` 或 `~/.acr/run/`，权限必须为 0700）中的 Unix socket，服务所有项目。`acr hook ...` 先交给守护进程执行；连不上、守护进程来自另一个构建（重新编译后会自动退出让位）或返回错误时，在本进程直接执行同一份代码，所以守护进程停掉不会削弱任何规则。请求超时等无法确认对方是否已执行的情况，会在本地再执行一次，代价是审计里可能多一条重复事件。`acr launch` 会确保守护进程在运行；`ACR_DAEMON=off` 关闭转发。守护进程按请求串行执行 Hook，并为每个请求套用客户端的 `ACR_LAUNCH_ID`、`ACR_UNATTENDED`。
- **热路径性能**（2026-10-20，WSL2，`tools/acrbench` 生成数据，每项 30 次）：
  - 事件库 schema v2 增加按类型和按会话的表达式索引（旧库打开时自动迁移，哈希链不变）。过滤查询显式指定索引（`INDEXED BY`），因为 SQLite 遇到"按 sequence 排序 + LIMIT"时会改走主键全表扫描。修复前在 10 万事件上，内存盘中的 `PreToolUse` 要 291 ms，且随事件数增长；修复后放行 19 ms、拒绝 9 ms、UserPromptSubmit 12 ms、SessionStart 26 ms，与事件总数无关。
  - 守护进程保持数据库连接，避免每个 Hook 进程首次写入时重建 WAL、关闭时执行 checkpoint；`PreToolUse` 的"提议 + 决策"合并为一个事务（`AppendBatch`）。
  - 本机真实磁盘一次 fsync 约 66 ms。在 `synchronous=FULL` 下，每个 Hook 进程还要承担两笔额外落盘：新连接首次写入时重建 WAL，关闭最后一个连接时做 checkpoint。当时 1 万事件、直接执行的 PreToolUse 放行 P50 为 245 ms。
  - 现已改为：事件库和记忆库使用 `synchronous=NORMAL`（投影库原本就是）；`acr` CLI 进程处理完 Hook 后不主动关闭数据库，直接退出。SQLite 把这种退出视同崩溃处理，已提交的事务都在 WAL 中，安全，同时省掉 checkpoint，WAL 也留给下一个进程复用；checkpoint 由 SQLite 在 WAL 达到 1000 页时自动执行。真实磁盘、1 万事件的 P50/P95：PreToolUse 放行直接执行 12.5/18.7 ms、经守护进程 11.7/14.2 ms；PostToolUse 9.4/15.8 ms；UserPromptSubmit 14.5/23.2 ms。满足"放行 P95 < 50 ms"，守护进程不再是性能前提。
  - 持久性边界：任何进程崩溃（Hook、守护进程、Agent）都不丢已提交事件；操作系统崩溃或断电可能丢最后几次提交，数据库不会损坏，哈希链仍然有效，只是链尾短一些。
  - `doctor` 和 `replay` 每次都完整校验哈希链，每条事件约 58 µs，主要花在规范化 JSON 上（10 万事件约 5.8 秒）。它们不在 Hook 热路径上。
  - 100 万事件规模未测：生成和测量耗时过长，按 z 的决定停止。
- **生命周期 Hook**：`acr hook <agent> lifecycle` 处理 Stop、SubagentStart、SubagentStop、SessionEnd，分别记为 `turn.stopped`、`subagent.started`、`subagent.stopped`、`session.ended`；SessionEnd 时立即做覆盖对账，不必等下次启动。子 Agent 的工具调用在动作的 `attributes.subagent_id` 中标出子 Agent。输入格式来自 Claude Code 2.1.303 和 Codex CLI 0.148.0 的真实采集（`internal/adapter/testdata/live`，已匿名化）。实测要点：两个宿主的子 Agent 工具调用都沿用父会话的 `session_id` 并带 `agent_id`；Claude 会把后台任务完成通知作为 UserPromptSubmit 提交，ACR 记为 `host.notification`，不算用户的话；Codex 的子 Agent 指令是加密的，ACR 看不到；Codex 0.148.0 的项目 Hook 未经信任时不执行，`codex exec` 下的 `permission_mode` 为 `bypassPermissions`。目前只记录，不做 SubagentStart 上下文注入和 Stop 完成条件检查。
- **Hook 静默检测**：`acr launch` 结束时统计本次启动内由 Hook 记录的事件（带本次启动 ID、且不是 Guarded 服务自己的事件），写入 `runtime.launch_ended` 的 `hook_events`。为 0 时立即在终端警告，`acr doctor` 也会对最近一次启动报告 `WARN hooks`，避免 Hook 被禁用、未受信任或持续失败时静默冒充正常。`acr doctor` 对每个 Agent 的 Hook 只有在该 Agent 的 Hook 真正记录过事件后才报 OK，并给出最近一次记录时间；Guarded 服务的事件不算。

- **Guarded 模式（Claude Code 与 Codex CLI）**：`acr launch --mode guarded claude` 保持现有 Claude 防线。Codex 0.148.0 的 `code_mode_host` 已完成源码审查：裸 V8 无文件、进程和网络宿主接口，嵌套原生工具与直接调用共用 router、审批和沙箱链路；P1 原生写入与 P2 进程绕过实测通过。最终配置只对 `mcp_servers.acr` 设置 `default_tools_approval_mode="approve"`，全局原生工具继续使用 `approval_policy=never` 和 read-only 沙箱。非交互 `codex exec` 正向实测完成 `action.denied → repair.* → verification.passed → action.passed → action.completed`，测试和部署产物均生成，事件哈希链通过且会话没有新增 coverage gap，因此 Codex 总闸已打开。实测覆盖的是非交互 `exec`；交互路径使用同一组参数，但未单独实测。交互式 `review`、`help` 等单字提示词仍会由 ACR 加 `--`，避免误解析成子命令。Codex 其他版本必须重新认证。

尚未实现：`DecisionRepair` 自动生成计划、跨主机的锁协调，以及 Native 模式下 PostToolUse 自动回滚和事务工作区。Codex Guarded 0.148.0 的非交互完整工具路由已通过认证；Claude Guarded 的真实验证状态见其独立能力记录。Native Hook 的真实会话已验证契约上下文、PreToolUse/PostToolUse 和最终对账，但 Hook 回调失败仍可能 fail-open，不能替代 Guarded 边界。
