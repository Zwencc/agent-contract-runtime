<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NButton, NSkeleton } from 'naive-ui'
import VChart from 'vue-echarts'
import { use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { BarChart, PieChart } from 'echarts/charts'
import { GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
import PageHeader from '../components/PageHeader.vue'
import MetricCard from '../components/MetricCard.vue'
import SectionCard from '../components/SectionCard.vue'
import { dataSource } from '../services'
import type { Overview } from '../types/domain'
import { localeText } from '../i18n'

use([CanvasRenderer, BarChart, PieChart, GridComponent, LegendComponent, TooltipComponent])
const data = ref<Overview | null>(null)
onMounted(async () => { data.value = await dataSource.overview() })
const activityOption = computed(() => ({ backgroundColor: 'transparent', tooltip: { trigger: 'axis' }, legend: { top: 0, right: 0, textStyle: { color: '#81958d', fontSize: 9 } }, grid: { left: 8, right: 8, top: 34, bottom: 4, containLabel: true }, xAxis: { type: 'category', data: data.value?.activity.map(x => x.time), axisLine: { lineStyle: { color: '#23342e' } }, axisLabel: { color: '#71847c', fontSize: 9 } }, yAxis: { type: 'value', splitLine: { lineStyle: { color: 'rgba(153,190,176,.08)' } }, axisLabel: { color: '#71847c', fontSize: 9 } }, series: [
  { name: localeText('Passed'), type: 'bar', stack: 'actions', data: data.value?.activity.map(x => x.passed), itemStyle: { color: '#57e3b4', borderRadius: [3,3,0,0] } },
  { name: localeText('Repaired'), type: 'bar', stack: 'actions', data: data.value?.activity.map(x => x.repaired), itemStyle: { color: '#f4c76b' } },
  { name: localeText('Denied'), type: 'bar', stack: 'actions', data: data.value?.activity.map(x => x.denied), itemStyle: { color: '#ff7f87' } }
]}))
</script>

<template>
  <div>
    <PageHeader page="overview" eyebrow="RUNTIME / LIVE"><NButton secondary size="small">{{ $lt('Export report') }}</NButton><NButton type="primary" size="small">{{ $lt('Open active session') }}</NButton></PageHeader>
    <template v-if="data">
      <div class="metric-grid"><MetricCard v-for="metric in data.metrics" :key="metric.label" :metric="metric" /></div>
      <div class="dashboard-grid">
        <div class="stack">
          <SectionCard :title="$lt('Runtime activity')" :subtitle="$lt('Policy decisions and repairs across the last five hours')"><VChart class="chart" :option="activityOption" autoresize /></SectionCard>
          <SectionCard :title="$lt('Needs attention')" :subtitle="$lt('Ranked by safety impact and evidence quality')">
            <div class="attention-list"><RouterLink v-for="item in data.attention" :key="item.id" class="attention-item" :to="item.route"><i class="severity-line" :class="`severity-${item.severity}`"></i><div class="attention-copy"><strong>{{ item.title }}</strong><p>{{ item.detail }}</p></div><span class="muted">→</span></RouterLink></div>
          </SectionCard>
        </div>
        <div class="stack">
          <SectionCard :title="$lt('Enforcement boundary')" :subtitle="$lt('Current session guarantee')">
            <div class="hero-boundary"><div class="boundary-core"><strong>{{ data.guarantee }}</strong><span>{{ data.mode.toUpperCase() }}</span><small>{{ data.agent }}</small></div></div>
            <div class="mini-stats"><div><strong>3</strong><span>{{ $lt('MCP tools') }}</span></div><div><strong>0</strong><span>{{ $lt('Native write') }}</span></div><div><strong>11s</strong><span>{{ $lt('Last scan') }}</span></div></div>
          </SectionCard>
          <SectionCard :title="$lt('Cross-agent continuity')" :subtitle="$lt('Observed sessions in this project')">
            <div class="plain-list"><div v-for="agent in data.agentShare" :key="agent.name" class="plain-row"><div class="attention-copy"><strong>{{ agent.name }}</strong><p>{{ agent.value }}% {{ $lt('of recent actions') }}</p></div><div style="width:110px"><div class="progress-bar"><i :style="{ width: `${agent.value}%` }"></i></div></div></div></div>
          </SectionCard>
        </div>
      </div>
    </template>
    <div v-else class="metric-grid"><NSkeleton v-for="n in 4" :key="n" height="140px" /></div>
  </div>
</template>

<style scoped>.attention-item { color: inherit; text-decoration: none; }</style>
