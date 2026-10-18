import type { AgentInsight, Contract, EventRecord, FailureCluster, GraphEdge, GraphNode, IntegrityStatus, InteractionSignal, MemoryRecord, Overview, ProjectInfo, RelianceSummary, Session } from '../types/domain'

export interface AcrDataSource {
  project(): Promise<ProjectInfo>
  overview(): Promise<Overview>
  sessions(): Promise<Session[]>
  events(sessionId?: string): Promise<EventRecord[]>
  contracts(): Promise<Contract[]>
  activateContract(id: string): Promise<Contract>
  simulateContract(id: string): Promise<{ matched: number; blocked: number; repaired: number }>
  memories(): Promise<MemoryRecord[]>
  transitionMemory(id: string, action: 'confirm' | 'contest' | 'retract' | 'archive'): Promise<MemoryRecord>
  compactMemories(): Promise<{ before: number; after: number; evidencePreserved: number }>
  graph(): Promise<{ nodes: GraphNode[]; edges: GraphEdge[] }>
  agentInsights(): Promise<AgentInsight[]>
  failures(): Promise<FailureCluster[]>
  reliance(): Promise<RelianceSummary>
  interactionSignals(): Promise<InteractionSignal[]>
  correctSignal(id: string): Promise<InteractionSignal>
  integrity(): Promise<IntegrityStatus>
}
