<script setup lang="ts">
import { darkTheme, NConfigProvider, NDialogProvider, NMessageProvider, NNotificationProvider } from 'naive-ui'
import { computed, watchEffect } from 'vue'
import { useAppStore } from './stores/app'

const app = useAppStore()
const theme = computed(() => app.dark ? darkTheme : null)
const themeOverrides = computed(() => ({
  common: {
    ...app.themeOverrides.common,
    ...(app.dark ? {} : {
      primaryColor: '#138a63',
      primaryColorHover: '#0f7655',
      primaryColorPressed: '#0b6247',
      infoColor: '#356fca',
      warningColor: '#a66b00',
      errorColor: '#c9434c'
    })
  }
}))

watchEffect(() => {
  document.documentElement.dataset.theme = app.dark ? 'dark' : 'light'
})
</script>

<template>
  <NConfigProvider :theme="theme" :theme-overrides="themeOverrides">
    <NDialogProvider>
      <NNotificationProvider>
        <NMessageProvider>
          <RouterView />
        </NMessageProvider>
      </NNotificationProvider>
    </NDialogProvider>
  </NConfigProvider>
</template>
