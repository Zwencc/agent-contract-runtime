import type { AcrDataSource } from './data-source'
import type { AgentInsight, Contract, EventRecord, FailureCluster, GraphEdge, GraphNode, IntegrityStatus, InteractionSignal, MemoryRecord, Overview, ProjectInfo, RelianceSummary, Session } from '../types/domain'

export class HttpDataSource implements AcrDataSource {
  constructor(private readonly base: string) {}

  private async request<T>(path: string, init?: RequestInit): Promise<T> {
    const response = await fetch(`${this.base}${path}`, {
      ...init,
      headers: { 'Accept': 'application/json', 'Content-Type': 'application/json', ...init?.headers }
    })
    if (!response.ok) {
      const body = await response.text()
      throw new Error(`ACR API ${response.status}: ${body || response.statusText}`)
    }
    return response.json() as Promise<T>
  }

  project() { return this.request<ProjectInfo>('/project') }
  overview() { return this.request<Overview>('/overview') }
  sessions() { return this.request<Session[]>('/sessions') }
  events(sessionId?: string) { return this.request<EventRecord[]>(`/events${sessionId ? `?session_id=${encodeURIComponent(sessionId)}` : ''}`) }
  contracts() { return this.request<Contract[]>('/contracts') }
  activateContract(id: string) { return this.request<Contract>(`/contracts/${encodeURIComponent(id)}/activate`, { method: 'POST', body: '{}' }) }
  simulateContract(id: string) { return this.request<{matched:number;blocked:number;repaired:number}>(`/contracts/${encodeURIComponent(id)}/simulate`, { method: 'POST', body: '{}' }) }
  memories() { return this.request<MemoryRecord[]>('/memories') }
  transitionMemory(id: string, action: 'confirm' | 'contest' | 'retract' | 'archive') { return this.request<MemoryRecord>(`/memories/${encodeURIComponent(id)}/${action}`, { method: 'POST', body: JSON.stringify({ reason: 'ACR Console 用户操作' }) }) }
  compactMemories() { return this.request<{before:number;after:number;evidencePreserved:number}>('/memories/compact/preview', { method: 'POST', body: '{}' }) }
  graph() { return this.request<{nodes:GraphNode[];edges:GraphEdge[]}>('/graph') }
  agentInsights() { return this.request<AgentInsight[]>('/insights/agents') }
  failures() { return this.request<FailureCluster[]>('/insights/failures') }
  reliance() { return this.request<RelianceSummary>('/insights/reliance') }
  interactionSignals() { return this.request<InteractionSignal[]>('/insights/interaction-state') }
  correctSignal(id: string) { return this.request<InteractionSignal>('/feedback/interaction-state', { method: 'POST', body: JSON.stringify({ observation_id: id, accurate: false }) }) }
  integrity() { return this.request<IntegrityStatus>('/integrity') }
}
