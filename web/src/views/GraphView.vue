<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, NSelect } from 'naive-ui'
import { Background } from '@vue-flow/background'
import { Controls } from '@vue-flow/controls'
import { MiniMap } from '@vue-flow/minimap'
import { VueFlow, type Edge, type Node } from '@vue-flow/core'
import PageHeader from '../components/PageHeader.vue'
import SectionCard from '../components/SectionCard.vue'
import { dataSource } from '../services'
import type { GraphNode } from '../types/domain'
import '@vue-flow/core/dist/style.css'
import '@vue-flow/core/dist/theme-default.css'
import '@vue-flow/controls/dist/style.css'
import '@vue-flow/minimap/dist/style.css'

const nodes = ref<Node[]>([])
const edges = ref<Edge[]>([])
const selected = ref<GraphNode | null>(null)
const colors: Record<string, string> = { module: '#57e3b4', contract: '#f4c76b', memory: '#78a9ff', verification: '#7ae3d5', failure: '#ff7f87', agent: '#b995ff' }

onMounted(async () => {
  const graph = await dataSource.graph()
  nodes.value = graph.nodes.map(node => ({ id: node.id, position: { x: node.x, y: node.y }, label: node.label, data: node, style: { background: '#10211b', color: '#e8f3ee', border: `1px solid ${colors[node.kind]}`, borderRadius: '9px', fontSize: '10px', padding: '10px 14px' } }))
  edges.value = graph.edges.map(edge => ({ id: edge.id, source: edge.source, target: edge.target, label: edge.label, animated: edge.label === 'constrained_by', style: { stroke: '#56746a' }, labelStyle: { fill: '#81958d', fontSize: 8 } }))
})
</script>

<template>
  <div>
    <PageHeader page="graph" eyebrow="STATE / EVIDENCE"><NButton secondary size="small">{{ $lt('Rebuild projection') }}</NButton></PageHeader>
    <div class="filter-row"><NSelect value="deploy" :options="[{label:$lt('Task-related: deploy.sh'),value:'deploy'},{label:$lt('Module: internal/memory'),value:'memory'}]" style="width:280px" /><NSelect value="1" :options="[{label:$lt('1 relationship hop'),value:'1'},{label:$lt('2 relationship hops'),value:'2'}]" /></div>
    <div class="graph-layout">
      <SectionCard :title="$lt('Evidence-anchored subgraph')" :subtitle="$lt('Expand from a task or module; the full repository is never drawn at once')"><div class="graph-canvas"><VueFlow :nodes="nodes" :edges="edges" fit-view-on-init @node-click="({node}) => selected = node.data as GraphNode"><Background :gap="22" :size="1" color="#294139" /><Controls /><MiniMap :node-color="node => colors[(node.data as GraphNode)?.kind] || '#57e3b4'" pannable zoomable /></VueFlow></div></SectionCard>
      <SectionCard :title="$lt('Selected evidence')" :subtitle="$lt('Every node and relationship must be traceable')"><template v-if="selected"><span class="node-kind" :style="{color:colors[selected.kind]}">{{ $lt(selected.kind) }}</span><h3>{{ selected.label }}</h3><p class="muted">{{ selected.detail }}</p><dl class="kv-grid"><dt>{{ $lt('Node ID') }}</dt><dd class="mono">{{ selected.id }}</dd><dt>{{ $lt('Evidence') }}</dt><dd>3 {{ $lt('immutable event refs') }}</dd><dt>{{ $lt('Last verified') }}</dt><dd>{{ $lt('sequence') }} 421</dd></dl><div class="callout" style="margin-top:18px">{{ $lt('This node is a projection. Delete the projection and ACR can rebuild it from source events.') }}</div></template><div v-else class="empty-state"><strong>{{ $lt('Select a node') }}</strong>{{ $lt('Inspect its origin, relationships, and verification state.') }}</div></SectionCard>
    </div>
  </div>
</template>

<style scoped>.graph-layout{display:grid;grid-template-columns:minmax(0,1fr) 330px;gap:16px}.graph-canvas{height:590px;background:var(--bg)}.node-kind{font:700 9px ui-monospace,monospace;text-transform:uppercase;letter-spacing:.14em}.section-card h3{margin:8px 0}</style>
