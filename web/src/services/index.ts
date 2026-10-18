import type { AcrDataSource } from './data-source'
import { HttpDataSource } from './http-data-source'
import { MockDataSource } from './mock-data-source'

export const dataSource: AcrDataSource = import.meta.env.VITE_ACR_DATA_SOURCE === 'mock'
  ? new MockDataSource()
  : new HttpDataSource(import.meta.env.VITE_ACR_API_BASE || '/api/v1')
