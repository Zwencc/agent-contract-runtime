<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, NProgress, NSwitch, useMessage } from 'naive-ui'
import PageHeader from '../components/PageHeader.vue'
import SectionCard from '../components/SectionCard.vue'
import StatusBadge from '../components/StatusBadge.vue'
import { useAppStore } from '../stores/app'
import { dataSource } from '../services'
import type { InteractionSignal } from '../types/domain'
import { localeText } from '../i18n'

const app = useAppStore()
const message = useMessage()
const signals = ref<InteractionSignal[]>([])
onMounted(async () => { signals.value = await dataSource.interactionSignals() })
const correct = async (id: string) => {
  const updated = await dataSource.correctSignal(id)
  const index = signals.value.findIndex(signal => signal.id === id)
  signals.value[index] = updated
  message.success(localeText('Correction stored locally as feedback, not a new psychological label'))
}
</script>

<template>
  <div><PageHeader page="interaction" eyebrow="OPTIONAL / LOCAL"><div class="privacy-switch"><span>{{ $lt('Local analysis') }}</span><NSwitch v-model:value="app.interactionStateEnabled" /></div></PageHeader><div class="callout warning" style="margin-bottom:16px"><strong>{{ $lt('Collaboration signals, not mental-health labels.') }}</strong> {{ $lt('This module cannot alter contracts, lower safety checks, change permissions, or produce medical conclusions.') }}</div>
    <template v-if="app.interactionStateEnabled"><div class="dashboard-grid"><SectionCard :title="$lt('Current signals')" :subtitle="$lt('Behavior-first, confidence shown explicitly')"><div class="signal-list"><article v-for="signal in signals" :key="signal.id" class="signal-card"><div class="signal-head"><strong>{{ $lt(signal.kind).replaceAll('_',' ') }}</strong><span>{{ signal.time }}</span></div><NProgress type="line" :percentage="Math.round(signal.confidence*100)" :show-indicator="false" :height="4" /><p>{{ signal.evidence }}</p><div class="signal-suggestion">→ {{ signal.suggestion }}</div><div class="signal-actions"><StatusBadge v-if="signal.corrected" status="healthy" label="user corrected" /><NButton v-else text type="primary" @click="correct(signal.id)">{{ $lt('This is inaccurate') }}</NButton></div></article></div></SectionCard><div class="stack"><SectionCard :title="$lt('Data boundary')" :subtitle="$lt('What this module may observe')"><div class="plain-list"><div class="plain-row"><span class="check">✓</span><div class="attention-copy"><strong>{{ $lt('Behavioral events') }}</strong><p>{{ $lt('Retries, reversals, failures, and agent switching.') }}</p></div></div><div class="plain-row"><span class="check">✓</span><div class="attention-copy"><strong>{{ $lt('Explicit phrases') }}</strong><p>{{ $lt('Limited patterns such as “why did this fail again?”') }}</p></div></div><div class="plain-row"><span class="deny">×</span><div class="attention-copy"><strong>{{ $lt('No camera or microphone') }}</strong><p>{{ $lt('No facial, voice, biometric, or background collection.') }}</p></div></div></div></SectionCard><SectionCard :title="$lt('Adaptive help')" :subtitle="$lt('Safe responses only')"><p class="muted">{{ $lt('Signals may simplify explanations, suggest a replay, or reduce simultaneous choices. They never reduce contract enforcement.') }}</p><NButton block secondary>{{ $lt('Preview assistance policy') }}</NButton></SectionCard></div></div></template>
    <div v-else class="disabled-state"><div class="disabled-orbit">≈</div><h2>{{ $lt('Interaction state analysis is off') }}</h2><p>{{ $lt('No interaction-state observations are generated or retained. Existing runtime evidence remains unaffected.') }}</p><NButton type="primary" @click="app.interactionStateEnabled=true">{{ $lt('Enable locally') }}</NButton></div>
  </div>
</template>

<style scoped>.privacy-switch{display:flex;align-items:center;gap:10px;color:var(--muted);font-size:11px}.signal-list{display:grid;gap:12px}.signal-card{padding:15px;border:1px solid var(--line);border-radius:9px;background:rgba(255,255,255,.014)}.signal-head{display:flex;justify-content:space-between;margin-bottom:10px}.signal-head strong{text-transform:capitalize;font-size:12px}.signal-head span{color:var(--muted);font-size:9px}.signal-card p{color:var(--muted);font-size:10px}.signal-suggestion{padding:9px;background:rgba(87,227,180,.05);color:#a8c7ba;font-size:10px;border-radius:6px}.signal-actions{display:flex;justify-content:flex-end;margin-top:8px}.check{color:var(--green)}.deny{color:var(--red)}.disabled-state{min-height:460px;display:flex;flex-direction:column;align-items:center;justify-content:center;text-align:center;border:1px dashed var(--line);border-radius:14px;background:rgba(13,24,21,.5)}.disabled-state p{max-width:500px;color:var(--muted);line-height:1.7}.disabled-orbit{width:90px;height:90px;display:grid;place-items:center;margin-bottom:12px;border:1px solid rgba(87,227,180,.25);border-radius:50%;color:var(--green);font-size:35px;box-shadow:0 0 80px rgba(87,227,180,.08)}</style>
