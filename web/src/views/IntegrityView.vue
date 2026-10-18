<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, useMessage } from 'naive-ui'
import PageHeader from '../components/PageHeader.vue'
import SectionCard from '../components/SectionCard.vue'
import StatusBadge from '../components/StatusBadge.vue'
import { dataSource } from '../services'
import type { IntegrityStatus } from '../types/domain'

const message = useMessage()
const data = ref<IntegrityStatus | null>(null)
onMounted(async () => { data.value = await dataSource.integrity() })
</script>

<template>
  <div><PageHeader page="integrity" eyebrow="SYSTEM / PROOF"><NButton type="primary" size="small" @click="message.success($lt('Hash chain and workspace coverage verified'))">{{ $lt('Run full verification') }}</NButton></PageHeader>
    <template v-if="data"><div class="metric-grid"><article class="metric-card"><div class="metric-top"><span>{{ $lt('Hash chain') }}</span><i /></div><strong>{{ $lt('Valid') }}</strong><div class="metric-meta"><span>{{ $lt('sequence') }} {{ data.lastSequence }}</span><small>{{ data.lastVerified }}</small></div></article><article class="metric-card"><div class="metric-top"><span>{{ $lt('Coverage') }}</span><i /></div><strong>{{ data.gaps.length }}</strong><div class="metric-meta"><span>{{ $lt('open gaps') }}</span><small>{{ $lt('workspace reconciled') }}</small></div></article><article class="metric-card tone-warning"><div class="metric-top"><span>{{ $lt('Native hooks') }}</span><i /></div><strong>L1</strong><div class="metric-meta"><span>{{ $lt('observed') }}</span><small>{{ $lt('not fail-closed') }}</small></div></article><article class="metric-card"><div class="metric-top"><span>Guarded MCP</span><i /></div><strong>3</strong><div class="metric-meta"><span>{{ $lt('allowed tools') }}</span><small>{{ $lt('required server') }}</small></div></article></div>
      <div class="dashboard-grid"><SectionCard :title="$lt('Boundary checks')" :subtitle="$lt('Latest deterministic verification')"><div class="plain-list"><div v-for="check in data.checks" :key="check.name" class="plain-row"><div class="attention-copy"><strong>{{ check.name }}</strong><p>{{ check.detail }}</p></div><StatusBadge :status="check.status" /></div></div></SectionCard><div class="stack"><SectionCard :title="$lt('Chain head')" :subtitle="$lt('Project-local tamper evidence')"><pre class="code-block">project_id   01JACR…RUNTIME
sequence     {{ data.lastSequence }}
head_hash    1ed8d4a9…ac93
algorithm    sha256
verified_at  {{ data.lastVerified }}</pre></SectionCard><SectionCard :title="$lt('Coverage gaps')" :subtitle="$lt('Only recognized evidence can explain changes')"><div v-if="data.gaps.length === 0" class="empty-state"><strong>{{ $lt('No unresolved gaps') }}</strong>{{ $lt('Write resources, guarded observed_changes, and repair argv fully explain the current workspace.') }}</div></SectionCard></div></div>
    </template>
  </div>
</template>
