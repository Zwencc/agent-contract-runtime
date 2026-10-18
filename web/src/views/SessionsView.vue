<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NButton, NInput, NSelect } from 'naive-ui'
import PageHeader from '../components/PageHeader.vue'
import SectionCard from '../components/SectionCard.vue'
import StatusBadge from '../components/StatusBadge.vue'
import { dataSource } from '../services'
import type { Session } from '../types/domain'

const sessions = ref<Session[]>([])
const search = ref('')
const agent = ref<string | null>(null)
onMounted(async () => { sessions.value = await dataSource.sessions() })
const filtered = computed(() => sessions.value.filter(s => (!agent.value || s.agent === agent.value) && (!search.value || `${s.intent} ${s.id} ${s.files.join(' ')}`.toLowerCase().includes(search.value.toLowerCase()))))
</script>

<template>
  <div><PageHeader page="sessions" eyebrow="EVIDENCE / SESSIONS"><NButton type="primary" size="small">{{ $lt('Follow live session') }}</NButton></PageHeader>
    <div class="filter-row"><NInput v-model:value="search" :placeholder="$lt('Search intent, file, session…')" clearable /><NSelect v-model:value="agent" clearable :placeholder="$lt('All agents')" :options="[{label:'Claude Code',value:'claude-code'},{label:'Codex',value:'codex'}]" /></div>
    <SectionCard :title="$lt('Session history')" :subtitle="`${filtered.length} ${$lt('Sessions')} · ${$lt('ordered by project sequence')}`" flush>
      <table class="data-table"><thead><tr><th>{{ $lt('Session / Intent') }}</th><th>Agent</th><th>{{ $lt('Boundary') }}</th><th>{{ $lt('Actions') }}</th><th>{{ $lt('Files') }}</th><th>{{ $lt('Started') }}</th><th>{{ $lt('Status') }}</th><th></th></tr></thead><tbody>
        <tr v-for="session in filtered" :key="session.id"><td><div class="cell-title"><strong>{{ session.intent }}</strong><small class="mono">{{ session.id }} · {{ session.nativeId }}</small></div></td><td><div class="cell-title"><strong>{{ session.agent }}</strong><small v-if="session.previousAgent">{{ $lt('after agent') }} {{ session.previousAgent }}</small></div></td><td><StatusBadge :status="session.guarantee === 'L2' ? 'healthy' : 'warning'" :label="`${session.guarantee} ${session.mode}`" /></td><td><strong>{{ session.actions }}</strong><br><small class="muted">{{ session.denied }} {{ $lt('denied') }} · {{ session.repaired }} {{ $lt('repair count') }}</small></td><td><span class="mono">{{ session.files.length }}</span><br><small class="muted">{{ session.files.slice(0,2).join(', ') }}</small></td><td><span class="mono">{{ session.startedAt }}</span><br><small class="muted">{{ session.duration }}</small></td><td><StatusBadge :status="session.status" /></td><td><RouterLink :to="`/replay/${session.id}`"><NButton text type="primary">{{ $lt('Replay') }} →</NButton></RouterLink></td></tr>
      </tbody></table>
    </SectionCard>
  </div>
</template>
