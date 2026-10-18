<script setup lang="ts">
import type { Metric } from '../types/domain'
defineProps<{ metric: Metric }>()
</script>

<template>
  <article class="metric-card" :class="`tone-${metric.tone}`">
    <div class="metric-top"><span>{{ metric.label }}</span><i></i></div>
    <strong>{{ metric.value }}</strong>
    <div class="metric-meta"><span>{{ metric.delta }}</span><small>{{ metric.detail }}</small></div>
  </article>
</template>
