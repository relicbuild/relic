import { afterEach, describe, expect, it, vi } from 'vitest'

describe('useCopy', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    useToast().clear()
  })

  it('falls back to the copy command without the Clipboard API (plain HTTP)', async () => {
    vi.stubGlobal('isSecureContext', false)
    let copied = ''
    document.execCommand = vi.fn(() => {
      const area = document.querySelector('textarea')
      copied = area?.value.slice(area.selectionStart, area.selectionEnd) ?? ''
      return true
    })
    await useCopy()('the text', 'Copied')
    expect(copied).toBe('the text')
    expect(useToast().toasts.value.map(t => t.title)).toEqual(['Copied'])
    expect(document.querySelector('textarea')).toBeNull()
  })

  it('writes through the Clipboard API in a secure context (HTTPS)', async () => {
    vi.stubGlobal('isSecureContext', true)
    const writeText = vi.fn(async () => {})
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    document.execCommand = vi.fn(() => true)
    await useCopy()('the text', 'Copied')
    expect(writeText).toHaveBeenCalledWith('the text')
    expect(document.execCommand).not.toHaveBeenCalled()
    expect(useToast().toasts.value.map(t => t.title)).toEqual(['Copied'])
  })

  it('reports a copy the Clipboard API refused', async () => {
    vi.stubGlobal('isSecureContext', true)
    vi.stubGlobal('navigator', { clipboard: { writeText: vi.fn(async () => Promise.reject(new Error('denied'))) } })
    await useCopy()('x')
    expect(useToast().toasts.value.map(t => t.color)).toEqual(['error'])
  })

  it('reports a refused copy', async () => {
    vi.stubGlobal('isSecureContext', false)
    document.execCommand = vi.fn(() => false)
    await useCopy()('x')
    expect(useToast().toasts.value.map(t => t.color)).toEqual(['error'])
  })
})
