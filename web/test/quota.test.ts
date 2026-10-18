import { describe, expect, it } from 'vitest'
import { sizeInBytes, sizeInUnits } from '~/utils/quota'

const MiB = 1048576n

describe('quota sizes', () => {
  it.each([
    { name: 'an untouched size keeps its bytes', value: sizeInUnits(1000000000n, MiB), original: 1000000000n, want: 1000000000n },
    { name: 'a changed size is whole units', value: 1000, original: 1000000000n, want: 1000n * MiB },
    { name: 'a new size is whole units', value: 5, original: undefined, want: 5n * MiB },
    { name: 'no quota stays none', value: 0, original: 0n, want: 0n }
  ])('$name', ({ value, original, want }) => {
    expect(sizeInBytes(value, MiB, original)).toBe(want)
  })

  it('shows the whole units a size holds', () => {
    expect(sizeInUnits(1000000000n, MiB)).toBe(953)
  })
})
