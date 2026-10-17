// useCopy copies text to the clipboard and confirms with a short toast.
export function useCopy() {
  const toast = useToast()
  return async (text: string, what = 'Copied') => {
    try {
      await writeClipboard(text)
      toast.add({ title: what, icon: 'i-lucide-clipboard-check', duration: 1500 })
    } catch {
      toast.add({ title: 'Could not copy to the clipboard', description: 'Select the text and copy it instead.', color: 'error' })
    }
  }
}

async function writeClipboard(text: string) {
  if (navigator.clipboard && window.isSecureContext) {
    return await navigator.clipboard.writeText(text)
  }
  const area = document.createElement('textarea')
  area.value = text
  area.setAttribute('readonly', '')
  area.style.position = 'fixed'
  area.style.opacity = '0'
  // Inside a modal, the textarea must be too: its focus trap would take the
  // focus, and the selection, back.
  const host = document.activeElement?.closest('[role=dialog]') ?? document.body
  host.appendChild(area)
  area.select()
  try {
    if (!document.execCommand('copy')) {
      throw new Error('the copy command was refused')
    }
  } finally {
    area.remove()
  }
}
