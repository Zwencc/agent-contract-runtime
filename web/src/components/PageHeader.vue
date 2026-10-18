<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { localeText } from '../i18n'
const props = defineProps<{ page: string; eyebrow?: string }>()
const { tm } = useI18n()
const copy = () => tm(`page.${props.page}`) as string[]
</script>

<template>
  <header class="page-header">
    <div><span v-if="eyebrow" class="eyebrow">{{ localeText(eyebrow) }}</span><h1>{{ copy()[0] }}</h1><p>{{ copy()[1] }}</p></div>
    <div class="page-actions"><slot /></div>
  </header>
</template>
