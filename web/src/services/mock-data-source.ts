import type { AcrDataSource } from './data-source'
import type { AgentInsight, Contract, EventRecord, FailureCluster, GraphEdge, GraphNode, IntegrityStatus, InteractionSignal, MemoryRecord, Overview, ProjectInfo, RelianceSummary, Session } from '../types/domain'
import { localeText } from '../i18n'

const pause = () => new Promise(resolve => setTimeout(resolve, 120))
const protocolFields = new Set(['id', 'type', 'agent', 'status', 'state', 'mode', 'priority', 'failureMode', 'kind', 'scope', 'command', 'previousHash', 'hash', 'risk', 'outcome', 'candidate', 'fingerprint', 'route', 'nativeId', 'guarantee', 'hashChain', 'coverage', 'hooks', 'mcp'])
const clone = <T>(value: T): T => {
  const result = structuredClone(value)
  const localize = (item: unknown, field = ''): unknown => {
    if (typeof item === 'string') return localeText(item)
    if (Array.isArray(item)) return item.map(child => localize(child, field))
    if (item && typeof item === 'object') {
      for (const [key, child] of Object.entries(item)) {
        if (!protocolFields.has(key)) (item as Record<string, unknown>)[key] = localize(child, key)
      }
    }
    return item
  }
  return localize(result) as T
}

const contracts: Contract[] = [
  { id: 'tests-before-deploy', name: 'Tests before deploy', description: '部署脚本执行前必须通过可重复验证计划。', version: 3, priority: 'P0', state: 'active', scope: 'workspace/**', hits: 12, repairs: 9, lastHit: '2 分钟前', failureMode: 'closed', applicability: 'input.command contains "deploy.sh"', instruction: 'Run repair:checks/unit before deployment.', verifier: 'plan: plans/checks.json' },
  { id: 'protect-prod-users', name: 'Protect production users', description: '禁止修改生产用户表结构或执行破坏性 SQL。', version: 2, priority: 'P0', state: 'active', scope: 'db/**', hits: 4, repairs: 0, lastHit: '昨天', failureMode: 'closed', applicability: 'command matches ALTER|DROP + prod_user', instruction: 'Reject destructive production schema mutations.', verifier: 'deterministic SQL matcher' },
  { id: 'generated-files', name: 'Generated files are immutable', description: '修改源文件而不是构建产物。', version: 1, priority: 'P2', state: 'candidate', scope: 'generated/**', hits: 3, repairs: 2, lastHit: '今天 09:41', failureMode: 'ask', applicability: 'resource.path startsWith generated/', instruction: 'Rewrite target to its source anchor.', verifier: 'source mapping exists' },
  { id: 'secret-output', name: 'Redact credentials', description: '事件写入前去除凭据和令牌。', version: 5, priority: 'P0', state: 'active', scope: '**', hits: 31, repairs: 31, lastHit: '刚刚', failureMode: 'closed', applicability: 'output contains secret pattern', instruction: 'Redact before persistence.', verifier: 'redaction scan' }
]

const memories: MemoryRecord[] = [
  { id: 'mem-001', claim: '部署前必须运行 plans/checks.json 中的 unit 验证计划。', kind: 'constraint', state: 'confirmed', priority: 0, confidence: 1, scope: 'deploy.sh', evidenceCount: 8, revisions: 4, updatedAt: '2 分钟前', usedBy: ['claude-code', 'codex'] },
  { id: 'mem-002', claim: 'internal/eventstore 使用 BEGIN IMMEDIATE 保证并发追加序列。', kind: 'decision', state: 'confirmed', priority: 1, confidence: 0.98, scope: 'internal/eventstore/**', evidenceCount: 5, revisions: 2, updatedAt: '昨天', usedBy: ['codex'] },
  { id: 'mem-003', claim: 'Codex Guarded 只认证 codex-cli 0.148.0。', kind: 'compatibility', state: 'active', priority: 1, confidence: 0.96, scope: 'cmd/acr/**', evidenceCount: 4, revisions: 3, updatedAt: '今天 10:22', usedBy: ['claude-code', 'codex'] },
  { id: 'mem-004', claim: '所有 archived 记忆都可以直接重新激活。', kind: 'assumption', state: 'contested', priority: 3, confidence: 0.42, scope: 'internal/memory/**', evidenceCount: 2, revisions: 5, updatedAt: '3 天前', usedBy: ['claude-code'] },
  { id: 'mem-005', claim: '旧的 generated/config.txt 是配置事实源。', kind: 'fact', state: 'stale', priority: 4, confidence: 0.31, scope: 'generated/**', evidenceCount: 1, revisions: 2, updatedAt: '6 天前', usedBy: ['codex'] }
]

const sessions: Session[] = [
  { id: 'ses-guarded-104', nativeId: '019a-codex', agent: 'codex', guarantee: 'L2', mode: 'guarded', status: 'active', intent: 'Run ./deploy.sh and report its output', startedAt: '10:42:18', duration: '03:18', actions: 8, denied: 1, repaired: 1, files: ['tested.log', 'deployed.txt'], complete: true, previousAgent: 'claude-code' },
  { id: 'ses-claude-103', nativeId: 'claude-a81f', agent: 'claude-code', guarantee: 'L2', mode: 'guarded', status: 'completed', intent: 'Implement checks-before-deploy contract', startedAt: '09:18:02', duration: '12:44', actions: 23, denied: 2, repaired: 2, files: ['.acr/contracts/tests-before-deploy.json', 'plans/checks.json'], complete: true, previousAgent: 'codex' },
  { id: 'ses-native-102', nativeId: '0199-codex', agent: 'codex', guarantee: 'L1', mode: 'native', status: 'completed', intent: 'Correct configuration source', startedAt: '昨天 17:05', duration: '01:06', actions: 4, denied: 0, repaired: 1, files: ['src/config.txt'], complete: true },
  { id: 'ses-claude-101', nativeId: 'claude-741c', agent: 'claude-code', guarantee: 'L1', mode: 'native', status: 'failed', intent: 'Investigate SQLite lock regression', startedAt: '昨天 14:11', duration: '08:31', actions: 17, denied: 1, repaired: 0, files: ['internal/eventstore/store.go'], complete: false }
]

const events: EventRecord[] = [
  { sequence: 414, id: 'evt-414', type: 'intent.observed', time: '10:42:18.102', agent: 'codex', status: 'healthy', summary: 'Intent captured', detail: 'Run ./deploy.sh in this project and report its output.', previousHash: 'b042…988a', hash: 'f3ad…0d17' },
  { sequence: 415, id: 'evt-415', type: 'action.proposed', time: '10:42:20.641', agent: 'codex', status: 'unknown', summary: 'Command proposed', detail: 'Codex selected the only registered mutation channel.', command: './deploy.sh', resources: ['deploy.sh'], previousHash: 'f3ad…0d17', hash: '49e1…afa2' },
  { sequence: 416, id: 'evt-416', type: 'action.denied', time: '10:42:20.648', agent: 'codex', status: 'warning', summary: 'Missing verification evidence', detail: 'repair:checks/unit has no valid evidence for the current input digest.', contract: 'tests-before-deploy@3', command: './deploy.sh', previousHash: '49e1…afa2', hash: '7e2d…4490' },
  { sequence: 417, id: 'evt-417', type: 'repair.started', time: '10:42:20.652', agent: 'codex', status: 'warning', summary: 'Repair plan started', detail: 'Executing plans/checks.json using the guarded executor.', contract: 'tests-before-deploy@3', previousHash: '7e2d…4490', hash: '301c…cc41' },
  { sequence: 418, id: 'evt-418', type: 'repair.step_completed', time: '10:42:20.669', agent: 'codex', status: 'healthy', summary: 'Unit check completed', detail: 'Exit 0 · echo tested >> tested.log', command: 'echo tested >> tested.log', resources: ['tested.log'], previousHash: '301c…cc41', hash: '03a3…eba5' },
  { sequence: 419, id: 'evt-419', type: 'verification.passed', time: '10:42:20.673', agent: 'codex', status: 'healthy', summary: 'Evidence accepted', detail: 'Evidence repair:checks/unit matches the current src/** digest.', contract: 'tests-before-deploy@3', previousHash: '03a3…eba5', hash: 'ee21…772c' },
  { sequence: 420, id: 'evt-420', type: 'action.passed', time: '10:42:20.679', agent: 'codex', status: 'healthy', summary: 'Deployment released', detail: 'Second evaluation passed with fresh verification evidence.', contract: 'tests-before-deploy@3', command: './deploy.sh', previousHash: 'ee21…772c', hash: '57bb…48a9' },
  { sequence: 421, id: 'evt-421', type: 'action.completed', time: '10:42:20.691', agent: 'codex', status: 'healthy', summary: 'deploy-ok', detail: 'Exit 0 · observed_changes=["deployed.txt"]', command: './deploy.sh', resources: ['deployed.txt'], previousHash: '57bb…48a9', hash: '1ed8…ac93' }
]

export class MockDataSource implements AcrDataSource {
  async project(): Promise<ProjectInfo> { await pause(); return { name: 'agent-contract-runtime', workspace: '/path/to/agent-contract-runtime', projectId: 'proj_demo', workspaceId: 'ws_demo' } }
  async overview(): Promise<Overview> { await pause(); return clone({ guarantee: 'L2', mode: 'Guarded', agent: 'codex', metrics: [
    { label: 'Active contracts', value: 12, delta: '+2 this week', tone: 'healthy', detail: '9 hard · 3 advisory' },
    { label: 'Repairs succeeded', value: '94.2%', delta: '+3.1%', tone: 'healthy', detail: '49 of 52 repair plans' },
    { label: 'Memory health', value: '87%', delta: '3 need review', tone: 'warning', detail: '42 confirmed · 1 contested' },
    { label: 'Coverage gaps', value: 0, delta: 'continuous', tone: 'healthy', detail: 'last scan 11 seconds ago' }
  ], attention: [
    { id: 'a1', severity: 'high', title: 'Contested memory affects generated files', detail: 'Two claims disagree about the source of generated/config.txt.', route: '/memories' },
    { id: 'a2', severity: 'medium', title: 'Codex version pin is approaching upgrade', detail: 'Guarded remains certified only for codex-cli 0.148.0.', route: '/integrity' },
    { id: 'a3', severity: 'low', title: 'Repeated full analysis detected', detail: 'The memory module was scanned three times without input changes.', route: '/insights/failures' }
  ], activity: [
    { time: '08:00', passed: 8, repaired: 1, denied: 0 }, { time: '09:00', passed: 14, repaired: 3, denied: 1 }, { time: '10:00', passed: 22, repaired: 5, denied: 2 }, { time: '11:00', passed: 17, repaired: 2, denied: 1 }, { time: '12:00', passed: 26, repaired: 4, denied: 0 }
  ], agentShare: [{ name: 'Claude Code', value: 46 }, { name: 'Codex', value: 54 }] }) }
  async sessions() { await pause(); return clone(sessions) }
  async events(sessionId?: string) { await pause(); void sessionId; return clone(events) }
  async contracts() { await pause(); return clone(contracts) }
  async activateContract(id: string) { await pause(); const item = contracts.find(value => value.id === id); if (!item) throw new Error('contract not found'); item.state = 'active'; return clone(item) }
  async simulateContract(id: string) { await pause(); const item = contracts.find(value => value.id === id); if (!item) throw new Error('contract not found'); return { matched: item.hits + 4, blocked: Math.max(1, item.hits - item.repairs), repaired: item.repairs + 2 } }
  async memories() { await pause(); return clone(memories) }
  async transitionMemory(id: string, action: 'confirm' | 'contest' | 'retract' | 'archive') { await pause(); const item = memories.find(value => value.id === id); if (!item) throw new Error('memory not found'); item.state = action === 'confirm' ? 'confirmed' : action === 'contest' ? 'contested' : action === 'retract' ? 'retracted' : 'archived'; item.revisions += 1; return clone(item) }
  async compactMemories() { await pause(); return { before: 48, after: 31, evidencePreserved: 100 } }
  async graph(): Promise<{ nodes: GraphNode[]; edges: GraphEdge[] }> { await pause(); return clone({ nodes: [
    { id: 'deploy', label: 'deploy.sh', kind: 'module', x: 80, y: 170, detail: 'Deployment entry point' },
    { id: 'contract', label: 'tests-before-deploy', kind: 'contract', x: 360, y: 60, detail: 'P0 active contract' },
    { id: 'memory', label: 'Verification required', kind: 'memory', x: 360, y: 280, detail: 'Confirmed shared memory' },
    { id: 'plan', label: 'plans/checks.json', kind: 'verification', x: 680, y: 170, detail: 'Repeatable verification plan' },
    { id: 'codex', label: 'Codex', kind: 'agent', x: 80, y: 370, detail: 'Current guarded session' },
    { id: 'failure', label: 'Missing evidence', kind: 'failure', x: 680, y: 370, detail: 'Resolved by repair' }
  ], edges: [
    { id: 'e1', source: 'deploy', target: 'contract', label: 'constrained_by' }, { id: 'e2', source: 'memory', target: 'contract', label: 'derived_into' }, { id: 'e3', source: 'contract', target: 'plan', label: 'verified_by' }, { id: 'e4', source: 'codex', target: 'deploy', label: 'observed_in' }, { id: 'e5', source: 'failure', target: 'plan', label: 'resolved_by' }
  ] }) }
  async agentInsights(): Promise<AgentInsight[]> { await pause(); return clone([
    { agent: 'claude-code', sessions: 27, firstPass: 81, repairSuccess: 96, repeatedAnalysis: 8, verification: 93, unexplained: 0, medianActions: 14 },
    { agent: 'codex', sessions: 31, firstPass: 77, repairSuccess: 94, repeatedAnalysis: 13, verification: 96, unexplained: 0, medianActions: 11 }
  ]) }
  async failures(): Promise<FailureCluster[]> { await pause(); return clone([
    { id: 'fail-1', title: 'Deploy attempted before unit evidence', fingerprint: 'exec:*/deploy.sh + missing:checks/unit', occurrences: 5, agents: ['claude-code', 'codex'], firstSeen: '8 天前', lastSeen: '2 分钟前', evidence: [201, 289, 416], candidate: 'contract', status: 'resolved' },
    { id: 'fail-2', title: 'Generated output edited instead of source', fingerprint: 'write:generated/**', occurrences: 3, agents: ['codex'], firstSeen: '6 天前', lastSeen: '昨天', evidence: [118, 337], candidate: 'contract', status: 'open' },
    { id: 'fail-3', title: 'Memory module fully rescanned without input change', fingerprint: 'analysis:internal/memory + same_digest', occurrences: 3, agents: ['claude-code', 'codex'], firstSeen: '3 天前', lastSeen: '今天 09:41', evidence: [352, 376, 399], candidate: 'memory', status: 'open' }
  ]) }
  async reliance(): Promise<RelianceSummary> { await pause(); return clone({ known: 38, unknown: 17, overReliance: 10.5, underReliance: 7.9, appropriateAcceptance: 84.2, appropriateRejection: 89.5, unverifiedAcceptance: 21.8, byRisk: [
    { risk: 'R0', appropriate: 17, inappropriate: 1, unknown: 5 }, { risk: 'R1', appropriate: 12, inappropriate: 2, unknown: 6 }, { risk: 'R2', appropriate: 7, inappropriate: 2, unknown: 4 }, { risk: 'R3', appropriate: 2, inappropriate: 0, unknown: 2 }
  ], observations: [
    { id: 'rel-1', recommendation: 'Run the repeatable unit plan before deploy.', decision: 'Accepted after inspecting the contract evidence.', outcome: 'correct', risk: 'R3', evidence: 'events 416–421' },
    { id: 'rel-2', recommendation: 'Edit generated/config.txt directly.', decision: 'Accepted, then corrected to src/config.txt.', outcome: 'incorrect', risk: 'R1', evidence: 'session ses-native-102' },
    { id: 'rel-3', recommendation: 'Replace the canonical JSON encoder.', decision: 'Deferred pending benchmark.', outcome: 'unknown', risk: 'R2', evidence: 'no final verification' }
  ] }) }
  async interactionSignals(): Promise<InteractionSignal[]> { await pause(); return clone([
    { id: 'sig-1', kind: 'confusion', confidence: 0.78, time: '今天 09:43', evidence: '同一模块在输入摘要未变化时连续分析 3 次。', suggestion: '回放最近一次已验证的模块分析，并仅检查变化部分。' },
    { id: 'sig-2', kind: 'urgency', confidence: 0.64, time: '昨天 17:06', evidence: '高风险部署请求在 50 秒内重复提交 2 次。', suggestion: '显示最短安全路径，但继续保留 P0 验证要求。' }
  ]) }
  async correctSignal(id: string) { await pause(); const signals = await this.interactionSignals(); const signal = signals.find(value => value.id === id); if (!signal) throw new Error('signal not found'); signal.corrected = true; return signal }
  async integrity(): Promise<IntegrityStatus> { await pause(); return clone({ hashChain: 'healthy', coverage: 'healthy', hooks: 'warning', mcp: 'healthy', lastSequence: 421, lastVerified: '11 秒前', gaps: [], checks: [
    { name: 'Event hash chain', status: 'healthy', detail: '421 events verified from genesis to head.' },
    { name: 'Workspace coverage', status: 'healthy', detail: 'No unresolved changes after sequence 421.' },
    { name: 'Codex Guarded boundary', status: 'healthy', detail: 'codex-cli 0.148.0 · read-only · ACR MCP only.' },
    { name: 'Claude Guarded boundary', status: 'healthy', detail: 'Strict MCP config and tool allowlist active.' },
    { name: 'Native Hook attestation', status: 'warning', detail: 'Configured and observed, but not a fail-closed boundary.' }
  ] }) }
}
