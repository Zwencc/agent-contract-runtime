import { describe, expect, it } from 'vitest'
import { MockDataSource } from './mock-data-source'

describe('MockDataSource', () => {
  it('provides a complete repair chain with continuous project sequences', async () => {
    const source = new MockDataSource()
    const events = await source.events('ses-guarded-104')
    expect(events.map(event => event.sequence)).toEqual([414, 415, 416, 417, 418, 419, 420, 421])
    expect(events.map(event => event.type)).toEqual(expect.arrayContaining(['action.denied', 'repair.started', 'verification.passed', 'action.passed', 'action.completed']))
  })

  it('never converts unknown reliance outcomes into incorrect outcomes', async () => {
    const source = new MockDataSource()
    const reliance = await source.reliance()
    expect(reliance.unknown).toBeGreaterThan(0)
    expect(reliance.observations.filter(item => item.outcome === 'unknown')).toHaveLength(1)
  })

  it('records memory lifecycle transitions as new revisions', async () => {
    const source = new MockDataSource()
    const before = (await source.memories()).find(item => item.id === 'mem-005')!
    const after = await source.transitionMemory('mem-005', 'archive')
    expect(after.state).toBe('archived')
    expect(after.revisions).toBe(before.revisions + 1)
  })

  it('keeps the guarded boundary and project chain healthy', async () => {
    const source = new MockDataSource()
    const [overview, integrity] = await Promise.all([source.overview(), source.integrity()])
    expect(overview.guarantee).toBe('L2')
    expect(integrity.hashChain).toBe('healthy')
    expect(integrity.gaps).toEqual([])
  })
})
