<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NButton, NInput, NSelect, useMessage } from 'naive-ui'
import PageHeader from '../components/PageHeader.vue'
import SectionCard from '../components/SectionCard.vue'
import StatusBadge from '../components/StatusBadge.vue'
import { dataSource } from '../services'
import type { FailureCluster } from '../types/domain'

const message = useMessage()
const failures = ref<FailureCluster[]>([])
const search = ref('')
onMounted(async () => { failures.value = await dataSource.failures() })
const filtered = computed(() => failures.value.filter(failure => !search.value || `${failure.title} ${failure.fingerprint}`.toLowerCase().includes(search.value.toLowerCase())))
</script>

<template>
  <div><PageHeader page="failures" eyebrow="INSIGHTS / PATTERNS"><NButton type="primary" size="small" @click="message.success($lt('Failure projection rebuilt from source events'))">{{ $lt('Re-run analysis') }}</NButton></PageHeader>
    <div class="filter-row"><NInput v-model:value="search" :placeholder="$lt('Search failure fingerprint…')" /><NSelect value="open" :options="[{label:$lt('All statuses'),value:'all'},{label:$lt('Open only'),value:'open'}]" /></div>
    <div class="three-column failure-grid"><article v-for="failure in filtered" :key="failure.id" class="failure-card"><div class="failure-top"><StatusBadge :status="failure.status === 'open' ? 'warning' : 'healthy'" :label="failure.status" /><span>{{ failure.occurrences }} {{ $lt('occurrences') }}</span></div><h3>{{ failure.title }}</h3><code>{{ failure.fingerprint }}</code><dl class="kv-grid"><dt>Agent</dt><dd>{{ failure.agents.join(' · ') }}</dd><dt>{{ $lt('Observed') }}</dt><dd>{{ failure.firstSeen }} → {{ failure.lastSeen }}</dd><dt>{{ $lt('Evidence') }}</dt><dd class="mono">#{{ failure.evidence.join(' · #') }}</dd><dt>{{ $lt('Candidate') }}</dt><dd>{{ $lt(failure.candidate) }}</dd></dl><div class="failure-actions"><NButton text>{{ $lt('Inspect cluster') }}</NButton><NButton v-if="failure.status === 'open'" type="primary" secondary>{{ $lt('Create candidate') }}</NButton></div></article></div>
    <SectionCard :title="$lt('Detection rules')" :subtitle="$lt('Deterministic before model-assisted clustering')" style="margin-top:16px"><div class="three-column"><div class="callout">{{ $lt('Same normalized action repeatedly fails with an unchanged input digest.') }}</div><div class="callout">{{ $lt('A confirmed memory exists, but another agent repeats the recorded failure path.') }}</div><div class="callout">{{ $lt('A module is fully rescanned while its anchored source digest is unchanged.') }}</div></div></SectionCard>
  </div>
</template>

<style scoped>.failure-grid{align-items:start}.failure-card{padding:18px;border:1px solid var(--line);border-radius:12px;background:var(--panel)}.failure-top{display:flex;justify-content:space-between;align-items:center;color:var(--muted);font-size:9px}.failure-card h3{min-height:42px;font-size:13px}.failure-card>code{display:block;min-height:48px;padding:9px;background:#08110e;border-radius:6px;color:#8ea99e;font:9px/1.5 ui-monospace,monospace}.failure-card .kv-grid{margin-top:16px;grid-template-columns:80px 1fr}.failure-actions{display:flex;justify-content:space-between;align-items:center;margin-top:18px}</style>
