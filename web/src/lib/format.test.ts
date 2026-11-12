import { describe, expect, it } from 'vitest'
import { formatRemaining, shortAddress } from './format'

describe('formatRemaining', () => {
  it.each([
    [15 * 60_000, '15:00'],
    [61_500, '1:01'],
    [999, '0:00'],
    [-5_000, '0:00'],
  ])('%d ms → %s', (ms, want) => expect(formatRemaining(ms)).toBe(want))
})

describe('shortAddress', () => {
  it('keeps the start and end', () => {
    expect(shortAddress('0x3CC845A0014aFcB0D52F673E0a98039074F1474c')).toBe('0x3CC8…474c')
  })
})
