<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import VChart from 'vue-echarts'
import { use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { BarChart } from 'echarts/charts'
import { GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
import { NButton, NSwitch } from 'naive-ui'
import PageHeader from '../components/PageHeader.vue'
import SectionCard from '../components/SectionCard.vue'
import StatusBadge from '../components/StatusBadge.vue'
import { dataSource } from '../services'
import type { RelianceSummary } from '../types/domain'
import { localeText } from '../i18n'

use([CanvasRenderer, BarChart, GridComponent, LegendComponent, TooltipComponent])
const data = ref<RelianceSummary | null>(null)
const drill = ref(false)
onMounted(async () => { data.value = await dataSource.reliance() })
const option = computed(() => ({ tooltip:{trigger:'axis'},legend:{top:0,right:0,textStyle:{color:'#81958d',fontSize:9}},grid:{left:10,right:10,top:34,bottom:5,containLabel:true},xAxis:{type:'category',data:data.value?.byRisk.map(x=>x.risk),axisLine:{lineStyle:{color:'#23342e'}},axisLabel:{color:'#81958d'}},yAxis:{type:'value',splitLine:{lineStyle:{color:'rgba(153,190,176,.08)'}},axisLabel:{color:'#81958d'}},series:[{name:localeText('Appropriate'),type:'bar',stack:'total',data:data.value?.byRisk.map(x=>x.appropriate),itemStyle:{color:'#57e3b4'}},{name:localeText('Inappropriate'),type:'bar',stack:'total',data:data.value?.byRisk.map(x=>x.inappropriate),itemStyle:{color:'#ff7f87'}},{name:localeText('Unknown'),type:'bar',stack:'total',data:data.value?.byRisk.map(x=>x.unknown),itemStyle:{color:'#52665e'}}] }))
</script>

<template>
  <div><PageHeader page="reliance" eyebrow="HUMAN / CALIBRATION"><NButton secondary size="small">{{ $lt('Export private report') }}</NButton></PageHeader><div class="callout" style="margin-bottom:16px"><strong>{{ $lt('This is not a dependency diagnosis.') }}</strong> {{ $lt('ACR measures whether verified correct and incorrect recommendations were accepted appropriately. Unknown outcomes remain unknown.') }}</div>
    <template v-if="data"><div class="metric-grid"><article class="metric-card"><div class="metric-top"><span>{{ $lt('Appropriate acceptance') }}</span><i /></div><strong>{{ data.appropriateAcceptance }}%</strong><div class="metric-meta"><span>{{ $lt('known correct') }}</span><small>{{ data.known }} {{ $lt('known outcomes') }}</small></div></article><article class="metric-card"><div class="metric-top"><span>{{ $lt('Appropriate rejection') }}</span><i /></div><strong>{{ data.appropriateRejection }}%</strong><div class="metric-meta"><span>{{ $lt('known incorrect') }}</span><small>{{ $lt('evidence-backed') }}</small></div></article><article class="metric-card tone-warning"><div class="metric-top"><span>{{ $lt('Over-reliance') }}</span><i /></div><strong>{{ data.overReliance }}%</strong><div class="metric-meta"><span>{{ $lt('accepted incorrect') }}</span><small>{{ $lt('not acceptance rate') }}</small></div></article><article class="metric-card tone-warning"><div class="metric-top"><span>{{ $lt('Unknown outcomes') }}</span><i /></div><strong>{{ data.unknown }}</strong><div class="metric-meta"><span>{{ $lt('not classified') }}</span><small>{{ $lt('no forced labels') }}</small></div></article></div>
      <div class="dashboard-grid"><SectionCard :title="$lt('Calibration by action risk')" :subtitle="$lt('R0 read-only → R3 irreversible or production')"><VChart class="chart" :option="option" autoresize /></SectionCard><SectionCard :title="$lt('Reliance drill')" :subtitle="$lt('Explicit, sandboxed practice only')"><div class="drill-state"><div class="score-ring" style="--score:84%"><strong>84</strong></div><div><strong>{{ $lt('Calibration baseline') }}</strong><p>{{ $lt('Based on 38 known outcomes. No drill results affect contracts or permissions.') }}</p></div></div><div class="plain-row"><div class="attention-copy"><strong>{{ $lt('Enable practice mode') }}</strong><p>{{ $lt('Uses a fixed sandbox dataset. Never injects errors into a real project.') }}</p></div><NSwitch v-model:value="drill" /></div><NButton block :disabled="!drill" type="primary">{{ $lt('Start a 5-minute drill') }}</NButton></SectionCard></div>
      <SectionCard :title="$lt('Recent observations')" :subtitle="$lt('Recommendation + human decision + verified outcome')" flush style="margin-top:16px"><table class="data-table"><thead><tr><th>{{ $lt('Recommendation') }}</th><th>{{ $lt('Decision') }}</th><th>{{ $lt('Risk') }}</th><th>{{ $lt('Outcome') }}</th><th>{{ $lt('Evidence') }}</th></tr></thead><tbody><tr v-for="item in data.observations" :key="item.id"><td><strong>{{ item.recommendation }}</strong></td><td class="muted">{{ item.decision }}</td><td class="mono">{{ item.risk }}</td><td><StatusBadge :status="item.outcome === 'correct' ? 'healthy' : item.outcome === 'incorrect' ? 'critical' : 'unknown'" :label="item.outcome" /></td><td class="mono muted">{{ item.evidence }}</td></tr></tbody></table></SectionCard>
    </template>
  </div>
</template>

<style scoped>.drill-state{display:flex;align-items:center;gap:18px;padding:15px 0 24px}.drill-state p{color:var(--muted);font-size:10px;line-height:1.6}.drill-state .score-ring{flex:0 0 120px}</style>
