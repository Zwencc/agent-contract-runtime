<script setup lang="ts">
import { ref } from 'vue'
import { NButton, NInput, NSelect, NSwitch, useMessage } from 'naive-ui'
import PageHeader from '../components/PageHeader.vue'
import SectionCard from '../components/SectionCard.vue'
import { useAppStore } from '../stores/app'

const app = useAppStore()
const message = useMessage()
const retention = ref('30')
const model = ref('disabled')
const telemetry = ref(false)
</script>

<template>
  <div><PageHeader page="settings" eyebrow="LOCAL / PRIVATE"><NButton type="primary" size="small" @click="message.success($lt('Settings saved locally'))">{{ $lt('Save changes') }}</NButton></PageHeader><div class="settings-grid">
    <SectionCard :title="$lt('Appearance')" :subtitle="$lt('Stored in this browser only')"><div class="setting-row"><div><strong>{{ $lt('Dark interface') }}</strong><p>{{ $lt('Optimized for long-running development sessions.') }}</p></div><NSwitch v-model:value="app.dark" /></div><div class="setting-row"><div><strong>{{ $lt('Language') }}</strong><p>{{ $lt('Navigation and product explanations.') }}</p></div><NSelect :value="app.locale" :options="[{label:'简体中文',value:'zh-CN'},{label:'English',value:'en-US'}]" style="width:150px" @update:value="app.setLocale" /></div></SectionCard>
    <SectionCard :title="$lt('Privacy')" :subtitle="$lt('Local-first defaults')"><div class="setting-row"><div><strong>{{ $lt('Anonymous telemetry') }}</strong><p>{{ $lt('Disabled by default. Never includes prompts, traces, events, or scores.') }}</p></div><NSwitch v-model:value="telemetry" /></div><div class="setting-row"><div><strong>{{ $lt('Interaction-state retention') }}</strong><p>{{ $lt('Delete derived observations without changing runtime evidence.') }}</p></div><NSelect v-model:value="retention" :options="[{label:$lt('Session only'),value:'session'},{label:$lt('7 days'),value:'7'},{label:$lt('30 days'),value:'30'}]" style="width:150px" /></div><NButton secondary type="error">{{ $lt('Clear derived human-state data') }}</NButton></SectionCard>
    <SectionCard :title="$lt('Optional model provider')" :subtitle="$lt('Never used in the enforcement hot path')"><div class="setting-row"><div><strong>{{ $lt('Analysis provider') }}</strong><p>{{ $lt('For contract drafts, summaries, compaction candidates, and optional text classification.') }}</p></div><NSelect v-model:value="model" :options="[{label:$lt('Disabled'),value:'disabled'},{label:$lt('Local OpenAI-compatible'),value:'local'},{label:$lt('Custom provider'),value:'custom'}]" style="width:190px" /></div><div v-if="model !== 'disabled'" class="stack"><NInput placeholder="http://127.0.0.1:11434/v1" /><div class="callout warning">{{ $lt('Model output can create candidates only. It cannot activate contracts, mutate memory state, or change runtime permissions.') }}</div></div></SectionCard>
    <SectionCard :title="$lt('Data export')" :subtitle="$lt('All exports pass through the ACR redaction pipeline')"><div class="setting-row"><div><strong>{{ $lt('Evidence bundle') }}</strong><p>{{ $lt('Events, contracts, memories, and current integrity report.') }}</p></div><NButton secondary>{{ $lt('Preview export') }}</NButton></div><div class="setting-row"><div><strong>OpenTelemetry</strong><p>{{ $lt('Future-compatible export; disabled in the frontend prototype.') }}</p></div><NButton disabled>{{ $lt('Configure') }}</NButton></div></SectionCard>
  </div></div>
</template>

<style scoped>.settings-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:16px;align-items:start}.setting-row{display:flex;align-items:center;justify-content:space-between;gap:24px;padding:14px 0;border-bottom:1px solid var(--line)}.setting-row:first-child{padding-top:0}.setting-row:last-child{border-bottom:0}.setting-row strong{font-size:11px}.setting-row p{max-width:440px;margin:4px 0 0;color:var(--muted);font-size:10px;line-height:1.5}</style>
