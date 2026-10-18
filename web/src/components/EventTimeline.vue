<script setup lang="ts">
import { ref } from 'vue'
import type { EventRecord } from '../types/domain'
import StatusBadge from './StatusBadge.vue'

defineProps<{ events: EventRecord[] }>()
const selected = ref<number | null>(null)
</script>

<template>
  <div class="event-timeline">
    <article v-for="event in events" :key="event.sequence" class="event-row" :class="{ selected: selected === event.sequence }" @click="selected = selected === event.sequence ? null : event.sequence">
      <div class="event-rail"><span class="event-seq">{{ event.sequence }}</span><i :class="`event-${event.status}`"></i></div>
      <div class="event-main">
        <div class="event-heading"><strong>{{ event.type }}</strong><span>{{ event.time }}</span><StatusBadge :status="event.status" /></div>
        <h3>{{ event.summary }}</h3><p>{{ event.detail }}</p>
        <div class="event-tags"><code v-if="event.command">$ {{ event.command }}</code><span v-if="event.contract">◇ {{ event.contract }}</span><span v-for="resource in event.resources" :key="resource">▱ {{ resource }}</span></div>
        <div v-if="selected === event.sequence" class="event-proof"><div><span>previous_hash</span><code>{{ event.previousHash }}</code></div><div><span>event_hash</span><code>{{ event.hash }}</code></div><button>{{ $lt('Open raw evidence') }} ↗</button></div>
      </div>
    </article>
  </div>
</template>
