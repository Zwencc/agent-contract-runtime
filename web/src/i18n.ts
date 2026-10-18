import { createI18n } from 'vue-i18n'

const messages = {
  'zh-CN': {
    nav: { overview: '运行总览', sessions: '会话', replay: '证据回放', contracts: '契约', memories: '记忆', graph: '项目图', agents: 'Agent 对比', failures: '重复失败', reliance: '依赖校准', interaction: '交互状态', integrity: '完整性', settings: '设置' },
    common: { live: '实时', healthy: '健康', degraded: '降级', evidence: '证据', inspect: '查看', project: '项目', workspace: '工作区', mock: '演示数据', unknown: '未知', updated: '刚刚更新' },
    page: {
      overview: ['运行总览', '现在是否安全、发生了什么、哪里需要处理。'],
      sessions: ['会话浏览器', '跨 Claude Code 与 Codex 查看连续工作。'],
      replay: ['证据回放', '沿不可变事件链查看意图、动作、修正和结果。'],
      contracts: ['契约工作台', '模拟、审阅并激活可执行约束。'],
      memories: ['记忆治理', '确认、纠正和压缩跨 Agent 项目记忆。'],
      graph: ['项目状态图', '从代码、契约、记忆和验证证据理解项目。'],
      agents: ['Agent 对比', '只基于当前项目事实比较 Agent 行为。'],
      failures: ['重复失败', '找到重复踩坑和无效的全量分析。'],
      reliance: ['依赖校准', '衡量是否在正确的时候接受或拒绝 Agent 建议。'],
      interaction: ['交互状态', '可选、本地、非诊断性的协作状态辅助。'],
      integrity: ['完整性中心', '验证哈希链、覆盖对账和运行时边界。'],
      settings: ['设置', '控制本地隐私、显示和实验能力。']
    }
  },
  'en-US': {
    nav: { overview: 'Overview', sessions: 'Sessions', replay: 'Replay', contracts: 'Contracts', memories: 'Memories', graph: 'Project Graph', agents: 'Agent Comparison', failures: 'Repeated Failures', reliance: 'Reliance', interaction: 'Interaction State', integrity: 'Integrity', settings: 'Settings' },
    common: { live: 'Live', healthy: 'Healthy', degraded: 'Degraded', evidence: 'Evidence', inspect: 'Inspect', project: 'Project', workspace: 'Workspace', mock: 'Demo data', unknown: 'Unknown', updated: 'Updated just now' },
    page: {
      overview: ['Runtime overview', 'Is the boundary healthy, what happened, and what needs attention?'],
      sessions: ['Session browser', 'Follow continuous work across Claude Code and Codex.'],
      replay: ['Evidence replay', 'Trace intent, action, repair, and outcome along the immutable event chain.'],
      contracts: ['Contract workbench', 'Simulate, review, and activate executable constraints.'],
      memories: ['Memory governance', 'Confirm, correct, and compact cross-agent project memory.'],
      graph: ['Project state graph', 'Understand the project through code, contracts, memories, and evidence.'],
      agents: ['Agent comparison', 'Compare agents using facts observed in this project only.'],
      failures: ['Repeated failures', 'Find recurring mistakes and redundant full analysis.'],
      reliance: ['Reliance calibration', 'Measure whether agent advice is accepted or rejected appropriately.'],
      interaction: ['Interaction state', 'Optional, local, non-diagnostic collaboration signals.'],
      integrity: ['Integrity center', 'Verify the hash chain, coverage, and runtime boundary.'],
      settings: ['Settings', 'Control local privacy, display, and experimental capabilities.']
    }
  }
}

const storedLocale = typeof localStorage === 'undefined' ? 'zh-CN' : localStorage.getItem('acr-locale') || 'zh-CN'
const i18n = createI18n({ legacy: false, locale: storedLocale, fallbackLocale: 'en-US', messages })

const zhText: Record<string, string> = {
  'Tests before deploy': '部署前测试', 'Protect production users': '保护生产用户', 'Generated files are immutable': '生成文件不可直接修改', 'Redact credentials': '凭据脱敏',
  'Run repair:checks/unit before deployment.': '部署前运行 repair:checks/unit。', 'Reject destructive production schema mutations.': '拒绝破坏性的生产表结构变更。', 'Rewrite target to its source anchor.': '将修改目标重写到对应源文件。', 'Redact before persistence.': '持久化前进行脱敏。',
  'deterministic SQL matcher': '确定性 SQL 匹配器', 'source mapping exists': '源文件映射存在', 'redaction scan': '脱敏扫描',
  'Run ./deploy.sh and report its output': '运行 ./deploy.sh 并报告输出', 'Implement checks-before-deploy contract': '实现部署前检查契约', 'Correct configuration source': '纠正配置事实源', 'Investigate SQLite lock regression': '调查 SQLite 锁回归',
  'Intent captured': '已捕获意图', 'Run ./deploy.sh in this project and report its output.': '在当前项目运行 ./deploy.sh 并报告输出。', 'Command proposed': '已提出命令', 'Codex selected the only registered mutation channel.': 'Codex 选择了唯一注册的变更通道。',
  'Missing verification evidence': '缺少验证证据', 'repair:checks/unit has no valid evidence for the current input digest.': 'repair:checks/unit 对当前输入摘要没有有效证据。', 'Repair plan started': '修正计划已启动', 'Executing plans/checks.json using the guarded executor.': '正在使用 Guarded 执行器运行 plans/checks.json。',
  'Unit check completed': '单元检查已完成', 'Evidence accepted': '证据已接受', 'Evidence repair:checks/unit matches the current src/** digest.': '证据 repair:checks/unit 与当前 src/** 摘要匹配。', 'Deployment released': '已允许部署', 'Second evaluation passed with fresh verification evidence.': '使用最新验证证据进行的第二次评估已通过。',
  'Active contracts': '生效契约', '+2 this week': '本周新增 2 条', '9 hard · 3 advisory': '9 条硬约束 · 3 条建议', 'Repairs succeeded': '修正成功率', '49 of 52 repair plans': '52 个修正计划成功 49 个', 'Memory health': '记忆健康度', '3 need review': '3 条需要审阅', '42 confirmed · 1 contested': '42 条已确认 · 1 条有争议', 'Coverage gaps': '覆盖缺口', 'continuous': '连续', 'last scan 11 seconds ago': '最近扫描于 11 秒前',
  'Contested memory affects generated files': '争议记忆影响生成文件', 'Two claims disagree about the source of generated/config.txt.': '两条声明对 generated/config.txt 的事实源存在冲突。', 'Codex version pin is approaching upgrade': 'Codex 固定版本即将需要升级', 'Guarded remains certified only for codex-cli 0.148.0.': 'Guarded 目前仍只认证 codex-cli 0.148.0。', 'Repeated full analysis detected': '检测到重复全量分析', 'The memory module was scanned three times without input changes.': '记忆模块在输入未变化时被扫描了三次。',
  'Deployment entry point': '部署入口', 'P0 active contract': 'P0 生效契约', 'Verification required': '需要验证', 'Confirmed shared memory': '已确认的共享记忆', 'Repeatable verification plan': '可重复验证计划', 'Current guarded session': '当前 Guarded 会话', 'Missing evidence': '缺少证据', 'Resolved by repair': '已由修正流程解决',
  'Deploy attempted before unit evidence': '在单元测试证据前尝试部署', 'Generated output edited instead of source': '修改了生成产物而不是源文件', 'Memory module fully rescanned without input change': '输入未变化却再次全量分析记忆模块',
  'Run the repeatable unit plan before deploy.': '部署前运行可重复的单元验证计划。', 'Accepted after inspecting the contract evidence.': '查看契约证据后接受。', 'Edit generated/config.txt directly.': '直接修改 generated/config.txt。', 'Accepted, then corrected to src/config.txt.': '先接受，随后纠正为 src/config.txt。', 'Replace the canonical JSON encoder.': '替换规范 JSON 编码器。', 'Deferred pending benchmark.': '等待基准测试后再决定。', 'no final verification': '没有最终验证',
  'Event hash chain': '事件哈希链', '421 events verified from genesis to head.': '从创世记录到链头共验证 421 个事件。', 'Workspace coverage': '工作区覆盖对账', 'No unresolved changes after sequence 421.': '序号 421 后没有未解释变更。', 'Codex Guarded boundary': 'Codex Guarded 边界', 'codex-cli 0.148.0 · read-only · ACR MCP only.': 'codex-cli 0.148.0 · 只读沙箱 · 仅 ACR MCP。', 'Claude Guarded boundary': 'Claude Guarded 边界', 'Strict MCP config and tool allowlist active.': '严格 MCP 配置和工具白名单已生效。', 'Native Hook attestation': '原生 Hook 证明', 'Configured and observed, but not a fail-closed boundary.': '已经配置并观测到，但不是失败关闭边界。',
  'Export report': '导出报告', 'Open active session': '打开活跃会话', 'Runtime activity': '运行时活动', 'Policy decisions and repairs across the last five hours': '最近五小时的策略判断与修正',
  'Needs attention': '需要处理', 'Ranked by safety impact and evidence quality': '按安全影响和证据质量排序', 'Enforcement boundary': '强制边界', 'Current session guarantee': '当前会话保证等级',
  'Cross-agent continuity': '跨 Agent 连续性', 'Observed sessions in this project': '当前项目中观测到的会话', 'Passed': '已放行', 'Repaired': '已修正', 'Denied': '已拒绝',
  'Follow live session': '跟随实时会话', 'Search intent, file, session…': '搜索意图、文件或会话…', 'All agents': '全部 Agent', 'Session history': '会话历史',
  'Session / Intent': '会话 / 意图', 'Agent': 'Agent', 'Boundary': '边界', 'Actions': '操作', 'Files': '文件', 'Started': '开始时间', 'Status': '状态', 'Replay': '回放',
  'Export evidence': '导出证据', 'Compare path': '对比路径', 'All event types': '全部事件类型', 'Decisions only': '仅决策', 'Raw details': '原始详情', 'Causal event stream': '因果事件流',
  'Session proof': '会话证据', 'Coverage and chain state': '覆盖与哈希链状态', 'Hash chain': '哈希链', 'Coverage': '覆盖对账', 'Mutation channel': '变更通道', 'Outcome': '结果', 'Effective state after replay': '回放后的有效状态',
  'Deployment completed safely.': '部署已安全完成。', 'Required evidence was missing, generated by repair, verified against the current source digest, and then consumed exactly once.': '缺失的必要证据已由修正流程生成，按当前源码摘要验证，并且只消费了一次。',
  'Import': '导入', 'Compile contract': '编译契约', 'Search contract…': '搜索契约…', 'All states': '全部状态', 'Active': '生效', 'Candidate': '候选', 'Contract registry': '契约注册表',
  'Contract': '契约', 'State': '状态', 'Priority': '优先级', 'Scope': '范围', 'Effectiveness': '有效性', 'Failure': '失败策略', 'Inspect': '查看', 'Description': '描述', 'Applicability': '适用条件', 'Instruction': '行为指令', 'Verifier': '验证器',
  'matched': '命中', 'blocked': '阻止', 'repaired': '修正', 'Simulate on history': '在历史事件上模拟', 'Approve & activate': '审批并激活', 'Compile a contract candidate': '编译契约候选', 'Intent': '意图', 'Structure': '结构', 'Simulation': '模拟', 'Approval': '审批',
  'A model may create a candidate, but can never activate it.': '模型可以创建候选，但永远不能直接激活。', 'Cancel': '取消', 'Compile candidate': '编译候选', 'Historical simulation completed': '历史模拟已完成', 'Contract activated with a new audit event': '契约已激活，并生成新的审计事件',
  'Compact preview': '压缩预览', 'Add candidate': '新增候选', 'Confirmed': '已确认', 'Needs review': '需要审阅', 'Evidence refs': '证据引用', 'Cross-agent': '跨 Agent', 'Project memory': '项目记忆',
  'Search claim, scope, kind…': '搜索声明、范围或类型…', 'Confidence': '置信度', 'Evidence': '证据', 'Used by': '使用者', 'Govern': '治理', 'Memory evidence & lifecycle': '记忆证据支撑的生命周期',
  'Memory ID': '记忆 ID', 'Revision': '版本', 'Revision timeline': '版本时间线', 'Current': '当前', 'Evidence confirmed': '证据已确认', 'Candidate created': '候选已创建', 'Contest': '标记争议', 'Archive': '归档', 'Retract': '撤回', 'Confirm evidence': '确认证据',
  'Memory compaction preview': '记忆压缩预览', 'before': '压缩前', 'after': '压缩后', 'evidence preserved': '证据保留', 'Compaction creates superseding memories and preserves every source revision. Nothing is physically deleted.': '压缩会创建替代记忆并保留所有来源版本，不会物理删除任何记录。', 'Create candidates': '创建候选',
  'Rebuild projection': '重建投影', 'Task-related: deploy.sh': '任务相关：deploy.sh', 'Module: internal/memory': '模块：internal/memory', '1 relationship hop': '一层关系', '2 relationship hops': '两层关系', 'Evidence-anchored subgraph': '证据锚定子图',
  'Selected evidence': '选中证据', 'Every node and relationship must be traceable': '每个节点和关系都必须可追溯', 'Node ID': '节点 ID', 'Last verified': '最近验证', 'Select a node': '选择一个节点', 'Inspect its origin, relationships, and verification state.': '查看其来源、关系和验证状态。',
  'Behavior profile': '行为画像', 'Normalized metrics from verified project outcomes': '根据项目已验证结果标准化', 'Current read': '当前解读', 'Evidence-backed interpretation': '基于证据的解读', 'Comparable evidence': '可比较证据', 'Same project, current observation window': '同一项目、当前观测窗口',
  'Sessions': '会话数', 'First pass': '首次通过', 'Repair success': '修正成功率', 'Verification': '验证覆盖', 'Repeated analysis': '重复分析', 'Unexplained': '未解释变更', 'Median actions': '操作中位数',
  'Re-run analysis': '重新分析', 'Search failure fingerprint…': '搜索失败指纹…', 'All statuses': '全部状态', 'Open only': '仅未解决', 'occurrences': '次发生', 'Agents': 'Agent', 'Observed': '观测时间', 'Inspect cluster': '查看聚类', 'Create': '创建',
  'Detection rules': '检测规则', 'Deterministic before model-assisted clustering': '先确定性检测，再使用模型辅助聚类',
  'Export private report': '导出私有报告', 'This is not a dependency diagnosis.': '这不是依赖性诊断。', 'ACR measures whether verified correct and incorrect recommendations were accepted appropriately. Unknown outcomes remain unknown.': 'ACR 只衡量经过验证的正确或错误建议是否被恰当地接受；结果未知时仍保持未知。',
  'Appropriate acceptance': '恰当接受', 'Appropriate rejection': '恰当拒绝', 'Over-reliance': '过度依赖', 'Unknown outcomes': '未知结果', 'Calibration by action risk': '按操作风险校准', 'R0 read-only → R3 irreversible or production': 'R0 只读 → R3 不可逆或生产操作',
  'Reliance drill': '依赖校准练习', 'Explicit, sandboxed practice only': '仅限明确开启的沙箱练习', 'Calibration baseline': '校准基线', 'Enable practice mode': '启用练习模式', 'Start a 5-minute drill': '开始 5 分钟练习', 'Recent observations': '最近观测', 'Recommendation + human decision + verified outcome': '建议 + 人类决定 + 已验证结果', 'Recommendation': '建议', 'Decision': '决定', 'Risk': '风险',
  'Local analysis': '本地分析', 'Collaboration signals, not mental-health labels.': '这是协作信号，不是心理健康标签。', 'This module cannot alter contracts, lower safety checks, change permissions, or produce medical conclusions.': '该模块不能修改契约、降低安全检查、改变权限或生成医疗结论。', 'Current signals': '当前信号', 'Behavior-first, confidence shown explicitly': '行为信号优先，并明确显示置信度',
  'This is inaccurate': '判断不准确', 'Data boundary': '数据边界', 'What this module may observe': '该模块允许观测的内容', 'Behavioral events': '行为事件', 'Explicit phrases': '明确表达', 'No camera or microphone': '不使用摄像头或麦克风', 'Adaptive help': '自适应辅助', 'Safe responses only': '仅提供安全响应', 'Preview assistance policy': '预览辅助策略',
  'Interaction state analysis is off': '交互状态分析已关闭', 'No interaction-state observations are generated or retained. Existing runtime evidence remains unaffected.': '不会生成或保留交互状态观测，现有运行时证据不受影响。', 'Enable locally': '仅在本地启用',
  'Run full verification': '执行完整验证', 'Valid': '有效', 'Native hooks': '原生 Hook', 'Guarded MCP': 'Guarded MCP', 'Boundary checks': '边界检查', 'Latest deterministic verification': '最近一次确定性验证', 'Chain head': '链头', 'Project-local tamper evidence': '项目本地防篡改证据', 'No unresolved gaps': '没有未解决缺口',
  'Save changes': '保存设置', 'Appearance': '外观', 'Stored in this browser only': '仅保存在当前浏览器', 'Dark interface': '深色界面', 'Language': '语言', 'Privacy': '隐私', 'Local-first defaults': '本地优先默认值', 'Anonymous telemetry': '匿名遥测', 'Interaction-state retention': '交互状态保留期限', 'Session only': '仅当前会话', 'Clear derived human-state data': '清除派生的人类状态数据',
  'Optional model provider': '可选模型提供方', 'Never used in the enforcement hot path': '永不用于强制执行热路径', 'Analysis provider': '分析提供方', 'Disabled': '关闭', 'Local OpenAI-compatible': '本地 OpenAI 兼容服务', 'Custom provider': '自定义提供方', 'Data export': '数据导出', 'All exports pass through the ACR redaction pipeline': '所有导出都经过 ACR 脱敏管线', 'Evidence bundle': '证据包', 'Preview export': '预览导出', 'Configure': '配置',
  'healthy': '健康', 'warning': '警告', 'critical': '严重', 'unknown': '未知', 'active': '生效', 'completed': '完成', 'failed': '失败', 'confirmed': '已确认', 'stale': '已过期', 'contested': '有争议', 'retracted': '已撤回', 'archived': '已归档', 'candidate': '候选', 'open': '未解决', 'resolved': '已解决', 'correct': '正确', 'incorrect': '错误', 'Stale': '已过期'
  , 'CONTROL': '控制', 'INSIGHTS': '洞察', 'SYSTEM': '系统', 'Boundary enforced': '边界已强制执行', 'Search events, contracts, memories…': '搜索事件、契约或记忆…', 'Theme': '主题', 'MCP tools': 'MCP 工具', 'Native write': '原生写入', 'Last scan': '最近扫描', 'of recent actions': '的近期操作'
  , 'ordered by project sequence': '按项目序号排序', 'after agent': '接续自', 'denied': '次拒绝', 'repair count': '次修正'
  , 'contracts': '条契约', 'deterministic evaluation first': '确定性评估优先', 'hits': '次命中', 'Candidate compiled for review': '候选已编译，等待审阅', 'closed': '失败关闭', 'ask': '询问'
  , 'memories': '条记忆', 'evidence-backed lifecycle': '证据支撑的生命周期', 'Claim': '声明', 'refs': '条引用', 'revisions': '个版本', 'immutable references': '条不可变引用', 'event refs merged and sorted': '事件引用已合并并排序', 'derived from session evidence': '从会话证据派生', 'trusted': '可信', 'with evidence': '具有证据', 'attention': '需关注', 'contested or stale': '有争议或已过期', 'traceable': '可追溯', 'across current set': '当前集合总计', 'shared': '共享', 'used by 2 agents': '被两个 Agent 使用'
  , 'Project sequences 414–421 · click an event to inspect its proof': '项目序号 414–421 · 点击事件查看证据', 'Genesis → project head': '创世记录 → 项目链头', '2 observed workspace changes': '观测到 2 项工作区变更', 'only': '专用', 'verified': '已验证', 'complete': '完整', 'guarded': '受保护'
  , 'Open raw evidence': '打开原始证据', 'Expand from a task or module; the full repository is never drawn at once': '从任务或模块展开，不一次绘制整个仓库', 'immutable event refs': '条不可变事件引用', 'sequence': '序号', 'This node is a projection. Delete the projection and ACR can rebuild it from source events.': '该节点属于投影；即使删除，ACR 也能从源事件重建。', 'module': '模块', 'contract': '契约', 'memory': '记忆', 'verification': '验证', 'failure': '失败'
  , 'These comparisons describe observed behavior in this project. They are not global rankings of Claude Code or Codex.': '这些比较仅描述当前项目中观测到的行为，不是对 Claude Code 或 Codex 的全局排名。', 'Codex uses fewer actions': 'Codex 使用的操作更少', 'Median 11 vs 14, with slightly lower first-pass success.': '操作中位数为 11 比 14，但首次通过率略低。', 'Claude repairs more reliably': 'Claude 修正更稳定', '96% vs 94%; difference is not significant at this sample size.': '96% 对 94%；当前样本量下差异不显著。', 'No unexplained writes': '没有未解释写入', 'Both agents maintained complete Guarded coverage.': '两个 Agent 都保持了完整的 Guarded 覆盖。', 'observed': '已观测', 'low sample': '样本较少'
  , 'Failure projection rebuilt from source events': '已从源事件重建失败投影', 'Create candidate': '创建候选', 'Same normalized action repeatedly fails with an unchanged input digest.': '输入摘要未变化时，相同标准化操作重复失败。', 'A confirmed memory exists, but another agent repeats the recorded failure path.': '已有确认记忆，但另一个 Agent 仍重复了记录中的失败路径。', 'A module is fully rescanned while its anchored source digest is unchanged.': '锚定源码摘要未变化时仍对模块进行全量重复分析。'
  , 'known correct': '已知正确', 'known outcomes': '个已知结果', 'known incorrect': '已知错误', 'evidence-backed': '有证据支撑', 'accepted incorrect': '接受错误建议', 'not acceptance rate': '并非总体接受率', 'not classified': '未分类', 'no forced labels': '不强制标注', 'Based on 38 known outcomes. No drill results affect contracts or permissions.': '基于 38 个已知结果；练习结果不会影响契约或权限。', 'Uses a fixed sandbox dataset. Never injects errors into a real project.': '使用固定沙箱数据集，绝不向真实项目注入错误。'
  , 'confusion': '困惑', 'frustration': '挫败', 'urgency': '急迫', 'hesitation': '犹豫', 'distrust': '不信任', 'possible_overconfidence': '可能过度自信', 'high_cognitive_load': '高认知负担', 'user corrected': '用户已纠正', 'Retries, reversals, failures, and agent switching.': '重试、撤销、失败和 Agent 切换。', 'Limited patterns such as “why did this fail again?”': '仅识别“为什么又失败了”等有限表达。', 'No facial, voice, biometric, or background collection.': '不采集面部、声音、生物特征或后台数据。', 'Signals may simplify explanations, suggest a replay, or reduce simultaneous choices. They never reduce contract enforcement.': '信号可以简化解释、建议回放或减少同时选项，但永远不会降低契约强制等级。'
  , 'Hash chain and workspace coverage verified': '哈希链和工作区覆盖对账已验证', 'open gaps': '未解决缺口', 'workspace reconciled': '工作区已对账', 'not fail-closed': '非失败关闭', 'allowed tools': '允许的工具', 'required server': '必需服务'
  , 'Settings saved locally': '设置已保存到本地', 'Optimized for long-running development sessions.': '针对长时间开发会话优化。', 'Navigation and product explanations.': '导航和产品说明语言。', 'Disabled by default. Never includes prompts, traces, events, or scores.': '默认关闭，绝不包含提示词、Trace、事件或评分。', 'Delete derived observations without changing runtime evidence.': '删除派生观测，不修改运行时证据。', '7 days': '7 天', '30 days': '30 天', 'For contract drafts, summaries, compaction candidates, and optional text classification.': '用于契约草稿、摘要、压缩候选和可选文本分类。', 'Model output can create candidates only. It cannot activate contracts, mutate memory state, or change runtime permissions.': '模型输出只能创建候选，不能激活契约、改变记忆状态或修改运行时权限。', 'Events, contracts, memories, and current integrity report.': '事件、契约、记忆和当前完整性报告。', 'Future-compatible export; disabled in the frontend prototype.': '面向未来的兼容导出；前端原型中暂未启用。'
  , 'Write resources, guarded observed_changes, and repair argv fully explain the current workspace.': '写类工具 resources、Guarded observed_changes 和修正 argv 已完整解释当前工作区。', 'Repair': '修正', 'No repeat': '无重复', 'Appropriate': '恰当', 'Inappropriate': '不恰当', 'Unknown': '未知'
  , 'Memory confirmed; a new revision was recorded': '记忆已确认，并记录了新版本', 'Memory marked as contested; a new revision was recorded': '记忆已标记为有争议，并记录了新版本', 'Memory retracted; a new revision was recorded': '记忆已撤回，并记录了新版本', 'Memory archived; a new revision was recorded': '记忆已归档，并记录了新版本', 'Correction stored locally as feedback, not a new psychological label': '纠正结果已作为反馈保存在本地，不会生成新的心理标签', 'Switch to light theme': '切换到浅色主题', 'Switch to dark theme': '切换到深色主题', 'CONSOLE': '控制台', 'GUARDED': '受保护模式'
  , 'CONTROL / EXECUTABLE': '控制 / 可执行契约', 'CONTINUITY / GOVERNED': '连续性 / 受治理', 'HUMAN / CALIBRATION': '用户 / 校准', 'IMMUTABLE / SEQUENCE': '不可变 / 序列', 'STATE / EVIDENCE': '状态 / 证据', 'EVIDENCE / SESSIONS': '证据 / 会话', 'LOCAL / PRIVATE': '本地 / 私有', 'RUNTIME / LIVE': '运行时 / 实时', 'INSIGHTS / PATTERNS': '洞察 / 模式', 'OPTIONAL / LOCAL': '可选 / 本地', 'SYSTEM / PROOF': '系统 / 证明', 'INSIGHTS / PROJECT-LOCAL': '洞察 / 项目本地'
}

export function localeText(text: string): string {
  return i18n.global.locale.value === 'zh-CN' ? (zhText[text] || text) : text
}

export default i18n
