<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import VChart from 'vue-echarts'
import { use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { RadarChart } from 'echarts/charts'
import { LegendComponent, RadarComponent, TooltipComponent } from 'echarts/components'
import PageHeader from '../components/PageHeader.vue'
import SectionCard from '../components/SectionCard.vue'
import StatusBadge from '../components/StatusBadge.vue'
import { dataSource } from '../services'
import type { AgentInsight } from '../types/domain'
import { localeText } from '../i18n'

use([CanvasRenderer, RadarChart, RadarComponent, LegendComponent, TooltipComponent])
const agents = ref<AgentInsight[]>([])
onMounted(async () => { agents.value = await dataSource.agentInsights() })
const option = computed(() => ({ tooltip: {}, legend: { bottom: 0, textStyle: { color: '#81958d' } }, radar: { indicator: [{name:localeText('First pass'),max:100},{name:localeText('Repair'),max:100},{name:localeText('Verification'),max:100},{name:localeText('No repeat'),max:100},{name:localeText('Coverage'),max:100}], axisName: {color:'#81958d',fontSize:9}, splitLine:{lineStyle:{color:'rgba(153,190,176,.13)'}}, splitArea:{areaStyle:{color:['rgba(87,227,180,.015)','rgba(87,227,180,.035)']}}, axisLine:{lineStyle:{color:'rgba(153,190,176,.13)'}} }, series:[{type:'radar',data:agents.value.map((agent,index)=>({name:agent.agent,value:[agent.firstPass,agent.repairSuccess,agent.verification,100-agent.repeatedAnalysis,100-agent.unexplained],lineStyle:{color:index?'#78a9ff':'#57e3b4'},itemStyle:{color:index?'#78a9ff':'#57e3b4'},areaStyle:{color:index?'rgba(120,169,255,.1)':'rgba(87,227,180,.1)'}}))}] }))
</script>

<template>
  <div><PageHeader page="agents" eyebrow="INSIGHTS / PROJECT-LOCAL" /><div class="callout warning" style="margin-bottom:16px">{{ $lt('These comparisons describe observed behavior in this project. They are not global rankings of Claude Code or Codex.') }}</div>
    <div class="dashboard-grid"><SectionCard :title="$lt('Behavior profile')" :subtitle="$lt('Normalized metrics from verified project outcomes')"><VChart class="chart" :option="option" autoresize /></SectionCard><SectionCard :title="$lt('Current read')" :subtitle="$lt('Evidence-backed interpretation')"><div class="plain-list"><div class="plain-row"><div class="attention-copy"><strong>{{ $lt('Codex uses fewer actions') }}</strong><p>{{ $lt('Median 11 vs 14, with slightly lower first-pass success.') }}</p></div><StatusBadge status="healthy" label="observed" /></div><div class="plain-row"><div class="attention-copy"><strong>{{ $lt('Claude repairs more reliably') }}</strong><p>{{ $lt('96% vs 94%; difference is not significant at this sample size.') }}</p></div><StatusBadge status="unknown" label="low sample" /></div><div class="plain-row"><div class="attention-copy"><strong>{{ $lt('No unexplained writes') }}</strong><p>{{ $lt('Both agents maintained complete Guarded coverage.') }}</p></div><StatusBadge status="healthy" label="verified" /></div></div></SectionCard></div>
    <SectionCard style="margin-top:16px" :title="$lt('Comparable evidence')" :subtitle="$lt('Same project, current observation window')" flush><table class="data-table"><thead><tr><th>Agent</th><th>{{ $lt('Sessions') }}</th><th>{{ $lt('First pass') }}</th><th>{{ $lt('Repair success') }}</th><th>{{ $lt('Verification') }}</th><th>{{ $lt('Repeated analysis') }}</th><th>{{ $lt('Unexplained') }}</th><th>{{ $lt('Median actions') }}</th></tr></thead><tbody><tr v-for="agent in agents" :key="agent.agent"><td><strong>{{ agent.agent }}</strong></td><td>{{ agent.sessions }}</td><td>{{ agent.firstPass }}%</td><td>{{ agent.repairSuccess }}%</td><td>{{ agent.verification }}%</td><td>{{ agent.repeatedAnalysis }}%</td><td><StatusBadge :status="agent.unexplained ? 'critical' : 'healthy'" :label="String(agent.unexplained)" /></td><td>{{ agent.medianActions }}</td></tr></tbody></table></SectionCard>
  </div>
</template>
