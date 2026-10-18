<script setup lang="ts">
import { computed, h, onMounted, ref } from 'vue'
import { NButton, NDrawer, NDrawerContent, NInput, NModal, NSelect, NSteps, NStep, useMessage } from 'naive-ui'
import PageHeader from '../components/PageHeader.vue'
import SectionCard from '../components/SectionCard.vue'
import StatusBadge from '../components/StatusBadge.vue'
import { dataSource } from '../services'
import type { Contract } from '../types/domain'
import { localeText } from '../i18n'

const message = useMessage()
const contracts = ref<Contract[]>([])
const selected = ref<Contract | null>(null)
const search = ref('')
const state = ref<string | null>(null)
const simulation = ref<{matched:number;blocked:number;repaired:number}|null>(null)
const showDraft = ref(false)
const draftPrompt = ref('部署前必须运行全部单元测试')
onMounted(async () => { contracts.value = await dataSource.contracts() })
const filtered = computed(() => contracts.value.filter(c => (!state.value || c.state === state.value) && (!search.value || `${c.name} ${c.description}`.toLowerCase().includes(search.value.toLowerCase()))))
const simulate = async () => { if (!selected.value) return; simulation.value = await dataSource.simulateContract(selected.value.id); message.success(localeText('Historical simulation completed')) }
const activate = async () => { if (!selected.value) return; selected.value = await dataSource.activateContract(selected.value.id); const index = contracts.value.findIndex(c => c.id === selected.value?.id); if (index >= 0) contracts.value[index] = selected.value; message.success(localeText('Contract activated with a new audit event')) }
</script>

<template>
  <div><PageHeader page="contracts" eyebrow="CONTROL / EXECUTABLE"><NButton secondary size="small">{{ $lt('Import') }}</NButton><NButton type="primary" size="small" @click="showDraft=true">+ {{ $lt('Compile contract') }}</NButton></PageHeader>
    <div class="filter-row"><NInput v-model:value="search" :placeholder="$lt('Search contract…')" clearable/><NSelect v-model:value="state" clearable :placeholder="$lt('All states')" :options="[{label:$lt('Active'),value:'active'},{label:$lt('Candidate'),value:'candidate'}]" /></div>
    <SectionCard :title="$lt('Contract registry')" :subtitle="`${filtered.length} ${$lt('contracts')} · ${$lt('deterministic evaluation first')}`" flush><table class="data-table"><thead><tr><th>{{ $lt('Contract') }}</th><th>{{ $lt('State') }}</th><th>{{ $lt('Priority') }}</th><th>{{ $lt('Scope') }}</th><th>{{ $lt('Effectiveness') }}</th><th>{{ $lt('Failure') }}</th><th></th></tr></thead><tbody>
      <tr v-for="contract in filtered" :key="contract.id"><td><div class="cell-title"><strong>{{ contract.name }} <span class="muted">v{{ contract.version }}</span></strong><small>{{ contract.description }}</small></div></td><td><StatusBadge :status="contract.state" /></td><td><span class="mono">{{ contract.priority }}</span></td><td><code class="mono muted">{{ contract.scope }}</code></td><td><strong>{{ contract.hits }}</strong> {{ $lt('hits') }}<br><small class="muted">{{ contract.repairs }} {{ $lt('repaired') }} · {{ contract.lastHit }}</small></td><td><StatusBadge :status="contract.failureMode === 'closed' ? 'healthy' : 'warning'" :label="contract.failureMode" /></td><td><NButton text type="primary" @click="selected=contract;simulation=null">{{ $lt('Inspect') }} →</NButton></td></tr>
    </tbody></table></SectionCard>
    <NDrawer :show="!!selected" width="600" @update:show="value => { if(!value) selected=null }"><NDrawerContent v-if="selected" :title="selected.name" closable>
      <div class="drawer-panel"><div class="filter-row"><StatusBadge :status="selected.state"/><StatusBadge :status="selected.failureMode==='closed'?'healthy':'warning'" :label="`fail-${selected.failureMode}`"/><span class="mono muted">{{ selected.priority }} · v{{ selected.version }}</span></div>
        <dl class="kv-grid"><dt>{{ $lt('Description') }}</dt><dd>{{ selected.description }}</dd><dt>{{ $lt('Scope') }}</dt><dd class="mono">{{ selected.scope }}</dd><dt>{{ $lt('Applicability') }}</dt><dd><pre class="code-block">{{ selected.applicability }}</pre></dd><dt>{{ $lt('Instruction') }}</dt><dd>{{ selected.instruction }}</dd><dt>{{ $lt('Verifier') }}</dt><dd class="mono">{{ selected.verifier }}</dd></dl>
        <div v-if="simulation" class="three-column simulation"><div><strong>{{ simulation.matched }}</strong><span>{{ $lt('matched') }}</span></div><div><strong>{{ simulation.blocked }}</strong><span>{{ $lt('blocked') }}</span></div><div><strong>{{ simulation.repaired }}</strong><span>{{ $lt('repaired') }}</span></div></div>
        <div class="drawer-actions"><NButton @click="simulate">{{ $lt('Simulate on history') }}</NButton><NButton v-if="selected.state!=='active'" type="primary" @click="activate">{{ $lt('Approve & activate') }}</NButton></div>
      </div></NDrawerContent></NDrawer>
    <NModal v-model:show="showDraft" preset="card" :title="$lt('Compile a contract candidate')" style="width:650px"><NSteps :current="1" size="small"><NStep :title="$lt('Intent')"/><NStep :title="$lt('Structure')"/><NStep :title="$lt('Simulation')"/><NStep :title="$lt('Approval')"/></NSteps><p class="muted">{{ $lt('A model may create a candidate, but can never activate it.') }}</p><NInput v-model:value="draftPrompt" type="textarea" :rows="5"/><template #footer><div style="display:flex;justify-content:flex-end;gap:8px"><NButton @click="showDraft=false">{{ $lt('Cancel') }}</NButton><NButton type="primary" @click="showDraft=false;message.success($lt('Candidate compiled for review'))">{{ $lt('Compile candidate') }}</NButton></div></template></NModal>
  </div>
</template>

<style scoped>.drawer-actions{display:flex;justify-content:flex-end;gap:8px;margin-top:24px}.simulation{margin-top:20px}.simulation div{padding:13px;background:var(--panel-2);border-radius:8px;text-align:center}.simulation strong{display:block;font-size:20px}.simulation span{color:var(--muted);font-size:9px}</style>
