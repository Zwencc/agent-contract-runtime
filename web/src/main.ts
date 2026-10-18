import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import i18n, { localeText } from './i18n'
import './styles.css'

const app = createApp(App)
app.config.globalProperties.$lt = localeText
app.use(createPinia()).use(router).use(i18n).mount('#app')
