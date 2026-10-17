import type { Ref } from 'vue'

// useDebounced mirrors source after it has been stable for delay ms.
export function useDebounced<T>(source: Ref<T>, delay = 300): Ref<T> {
  const value = ref(source.value) as Ref<T>
  let timer: ReturnType<typeof setTimeout> | undefined
  watch(source, (v) => {
    clearTimeout(timer)
    timer = setTimeout(() => {
      value.value = v
    }, delay)
  })
  onScopeDispose(() => clearTimeout(timer))
  return value
}
