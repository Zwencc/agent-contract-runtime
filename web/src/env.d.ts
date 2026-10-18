/// <reference types="vite/client" />

export {}

interface ImportMetaEnv {
  readonly VITE_ACR_DATA_SOURCE?: 'mock' | 'http'
  readonly VITE_ACR_API_BASE?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}

declare module 'vue' {
  interface ComponentCustomProperties {
    $lt: (text: string) => string
  }
}
