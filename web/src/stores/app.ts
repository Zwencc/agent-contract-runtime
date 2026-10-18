import { defineStore } from 'pinia'

export const useAppStore = defineStore('app', {
  state: () => ({
    dark: localStorage.getItem('acr-theme') !== 'light',
    locale: (localStorage.getItem('acr-locale') || 'zh-CN') as 'zh-CN' | 'en-US',
    project: 'agent-contract-runtime',
    workspace: '/path/to/agent-contract-runtime',
    interactionStateEnabled: false,
    themeOverrides: {
      common: {
        primaryColor: '#57e3b4',
        primaryColorHover: '#7af1c8',
        primaryColorPressed: '#35c796',
        infoColor: '#78a9ff',
        warningColor: '#f4c76b',
        errorColor: '#ff7f87',
        borderRadius: '10px',
        fontFamily: "Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif"
      }
    }
  }),
  actions: {
    toggleTheme() {
      this.dark = !this.dark
      localStorage.setItem('acr-theme', this.dark ? 'dark' : 'light')
    },
    setLocale(locale: 'zh-CN' | 'en-US') {
      this.locale = locale
      localStorage.setItem('acr-locale', locale)
    }
  }
})
