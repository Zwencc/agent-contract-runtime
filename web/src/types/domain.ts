export type Health = 'healthy' | 'warning' | 'critical' | 'unknown'
export type GuaranteeLevel = 'L0' | 'L1' | 'L2' | 'L3'
export type AgentKind = 'claude-code' | 'codex'
export type EventActor = AgentKind | 'runtime' | 'human' | 'agent_session' | 'unknown'

export interface ProjectInfo {
  name: string
  workspace: string
  projectId: string
  workspaceId: string
}
export type MemoryState = 'candidate' | 'active' | 'confirmed' | 'stale' | 'contested' | 'superseded' | 'retracted' | 'archived'

export interface Metric {
  label: string
  value: string | number
  delta?: string
  tone: Health
  detail: string
}

export interface AttentionItem {
  id: string
  severity: 'high' | 'medium' | 'low'
  title: string
  detail: string
  route: string
}

export interface Overview {
  guarantee: GuaranteeLevel
  mode: string
  agent: AgentKind
  metrics: Metric[]
  attention: AttentionItem[]
  activity: Array<{ time: string; passed: number; repaired: number; denied: number }>
  agentShare: Array<{ name: string; value: number }>
}

export interface Session {
  id: string
  nativeId: string
  agent: AgentKind
  guarantee: GuaranteeLevel
  mode: 'guarded' | 'native'
  status: 'active' | 'completed' | 'failed'
  intent: string
  startedAt: string
  duration: string
  actions: number
  denied: number
  repaired: number
  files: string[]
  complete: boolean
  previousAgent?: AgentKind
}

export interface EventRecord {
  sequence: number
  id: string
  type: string
  time: string
  agent: EventActor
  status: Health
  summary: string
  detail: string
  contract?: string
  command?: string
  resources?: string[]
  previousHash: string
  hash: string
}

export interface Contract {
  id: string
  name: string
  description: string
  version: number
  priority: string
  state: 'draft' | 'candidate' | 'active' | 'inactive'
  scope: string
  hits: number
  repairs: number
  lastHit: string
  failureMode: 'open' | 'closed' | 'ask'
  applicability: string
  instruction: string
  verifier: string
}

export interface MemoryRecord {
  id: string
  claim: string
  kind: string
  state: MemoryState
  priority: number
  confidence: number
  scope: string
  evidenceCount: number
  revisions: number
  updatedAt: string
  usedBy: AgentKind[]
}

export interface GraphNode {
  id: string
  label: string
  kind: 'module' | 'contract' | 'memory' | 'verification' | 'failure' | 'agent'
  x: number
  y: number
  detail: string
}

export interface GraphEdge {
  id: string
  source: string
  target: string
  label: string
}

export interface AgentInsight {
  agent: AgentKind
  sessions: number
  firstPass: number
  repairSuccess: number
  repeatedAnalysis: number
  verification: number
  unexplained: number
  medianActions: number
}

export interface FailureCluster {
  id: string
  title: string
  fingerprint: string
  occurrences: number
  agents: AgentKind[]
  firstSeen: string
  lastSeen: string
  evidence: number[]
  candidate: 'contract' | 'memory' | 'none'
  status: 'open' | 'resolved' | 'ignored'
}

export interface RelianceSummary {
  known: number
  unknown: number
  overReliance: number
  underReliance: number
  appropriateAcceptance: number
  appropriateRejection: number
  unverifiedAcceptance: number
  byRisk: Array<{ risk: string; appropriate: number; inappropriate: number; unknown: number }>
  observations: Array<{ id: string; recommendation: string; decision: string; outcome: 'correct' | 'incorrect' | 'unknown'; risk: string; evidence: string }>
}

export interface InteractionSignal {
  id: string
  kind: 'confusion' | 'frustration' | 'urgency' | 'hesitation' | 'distrust' | 'possible_overconfidence' | 'high_cognitive_load'
  confidence: number
  time: string
  evidence: string
  suggestion: string
  corrected?: boolean
}

export interface IntegrityStatus {
  hashChain: Health
  coverage: Health
  hooks: Health
  mcp: Health
  lastSequence: number
  lastVerified: string
  gaps: Array<{ id: string; path: string; detectedAt: string; resolution: string }>
  checks: Array<{ name: string; status: Health; detail: string }>
}
