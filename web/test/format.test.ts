import { describe, expect, it } from 'vitest'
import { formatDay, shellWord } from '~/utils/format'

describe('shellWord', () => {
  it.each([
    { name: 'a plain word stays', value: 'lodash==4.17.21', want: 'lodash==4.17.21' },
    { name: 'a placeholder stays', value: '<token>', want: '<token>' },
    { name: 'a space is quoted', value: 'john doe', want: '\'john doe\'' },
    { name: 'a command separator is quoted', value: 'pkg==1.0; rm -rf ~', want: '\'pkg==1.0; rm -rf ~\'' },
    { name: 'a single quote is escaped', value: 'it\'s', want: '\'it\'\\\'\'s\'' }
  ])('$name', ({ value, want }) => {
    expect(shellWord(value)).toBe(want)
  })
})

describe('formatDay', () => {
  it('is the date alone, in any locale', () => {
    const iso = '2026-03-04T05:06:07Z'
    expect(formatDay(iso)).toBe(new Date(iso).toLocaleDateString())
    expect(formatDay(iso)).not.toContain(new Date(iso).toLocaleTimeString())
    expect(formatDay(undefined)).toBe('')
  })
})
