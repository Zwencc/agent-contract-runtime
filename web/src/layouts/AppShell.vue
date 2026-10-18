<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { NBadge, NButton, NDropdown, NInput, NTooltip } from 'naive-ui'
import { useAppStore } from '../stores/app'
import { dataSource } from '../services'

const app = useAppStore()
const route = useRoute()
const router = useRouter()
const { t, locale } = useI18n()
const collapsed = ref(false)

const groups = computed(() => [
  { label: '', items: [
    { key: 'overview', path: '/', icon: '⌁' }, { key: 'sessions', path: '/sessions', icon: '◫' }, { key: 'replay', path: '/replay', icon: '↻' }
  ] },
  { label: 'CONTROL', items: [
    { key: 'contracts', path: '/contracts', icon: '◇' }, { key: 'memories', path: '/memories', icon: '◉' }, { key: 'graph', path: '/graph', icon: '⌘' }
  ] },
  { label: 'INSIGHTS', items: [
    { key: 'agents', path: '/insights/agents', icon: '⇄' }, { key: 'failures', path: '/insights/failures', icon: '△' }, { key: 'reliance', path: '/insights/reliance', icon: '◎' }, { key: 'interaction', path: '/insights/interaction', icon: '≈' }
  ] },
  { label: 'SYSTEM', items: [
    { key: 'integrity', path: '/integrity', icon: '⬡' }, { key: 'settings', path: '/settings', icon: '⚙' }
  ] }
])

const active = (path: string) => path === '/' ? route.path === '/' : route.path.startsWith(path)
const localeOptions = [{ label: '简体中文', key: 'zh-CN' }, { label: 'English', key: 'en-US' }]
const setLocale = (key: string) => { app.setLocale(key as 'zh-CN' | 'en-US'); locale.value = key }
watch(() => app.locale, value => { locale.value = value }, { immediate: true })
onMounted(async () => {
  const project = await dataSource.project()
  app.project = project.name
  app.workspace = project.workspace
})
</script>

<template>
  <div class="app-shell" :class="{ 'sidebar-collapsed': collapsed }">
    <aside class="sidebar">
      <div class="brand" @click="router.push('/')">
        <div class="brand-mark"><span>A</span></div>
        <div v-if="!collapsed" class="brand-copy"><strong>ACR</strong><small>{{ $lt('CONSOLE') }}</small></div>
      </div>
      <nav class="nav-scroll">
        <section v-for="group in groups" :key="group.label" class="nav-group">
          <div v-if="group.label && !collapsed" class="nav-label">{{ $lt(group.label) }}</div>
          <button v-for="item in group.items" :key="item.key" class="nav-item" :class="{ active: active(item.path) }" @click="router.push(item.path)">
            <span class="nav-icon">{{ item.icon }}</span>
            <span v-if="!collapsed">{{ t(`nav.${item.key}`) }}</span>
            <span v-if="item.key === 'failures' && !collapsed" class="nav-count">2</span>
          </button>
        </section>
      </nav>
      <div class="sidebar-footer">
        <div class="boundary-card" :class="{ compact: collapsed }">
          <span class="pulse-dot"></span>
          <div v-if="!collapsed"><strong>L2 · {{ $lt('GUARDED') }}</strong><small>{{ $lt('Boundary enforced') }}</small></div>
        </div>
        <button class="collapse-button" @click="collapsed = !collapsed">{{ collapsed ? '›' : '‹' }}</button>
      </div>
    </aside>
    <main class="main-area">
      <header class="topbar">
        <div class="project-switcher">
          <span class="project-cube">◆</span>
          <div><strong>{{ app.project }}</strong><small>{{ app.workspace }}</small></div>
          <span class="chevron">⌄</span>
        </div>
        <div class="topbar-actions">
          <NInput class="command-search" size="small" :placeholder="$lt('Search events, contracts, memories…')"><template #prefix>⌕</template><template #suffix><kbd>⌘ K</kbd></template></NInput>
          <NTooltip trigger="hover"><template #trigger><NButton quaternary circle @click="app.toggleTheme">{{ app.dark ? '☼' : '◐' }}</NButton></template>{{ app.dark ? $lt('Switch to light theme') : $lt('Switch to dark theme') }}</NTooltip>
          <NDropdown :options="localeOptions" @select="setLocale"><NButton quaternary>{{ app.locale === 'zh-CN' ? '中文' : 'EN' }}</NButton></NDropdown>
          <NBadge dot type="success"><button class="agent-chip"><span>CODEX</span><small>0.148.0</small></button></NBadge>
        </div>
      </header>
      <div class="content-area"><RouterView /></div>
    </main>
  </div>
</template>
