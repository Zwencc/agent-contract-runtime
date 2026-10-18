<script setup lang="ts">
import type { Health } from '../types/domain'
defineProps<{ status: Health | string; label?: string }>()
</script>

<template><span class="status-badge" :class="`status-${status}`"><i></i>{{ $lt(label || status) }}</span></template>
