<script setup lang="ts">
defineProps<{ title: string; subtitle?: string; flush?: boolean }>()
</script>

<template>
  <section class="section-card" :class="{ flush }">
    <header><div><h2>{{ title }}</h2><p v-if="subtitle">{{ subtitle }}</p></div><slot name="action" /></header>
    <div class="section-body"><slot /></div>
  </section>
</template>
