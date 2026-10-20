<script setup lang="ts">
const props = withDefaults(defineProps<{
  words: string[]
  interval?: number
  delay?: number
}>(), {
  interval: 2800,
  delay: 0
})

const index = ref(0)
const width = ref<string>()
const sizes = useTemplateRef<HTMLElement>('sizes')

function measure() {
  const word = sizes.value?.children[index.value]
  if (word instanceof HTMLElement) {
    width.value = `${word.offsetWidth}px`
  }
}

let startTimer: ReturnType<typeof setTimeout> | undefined
let rotateTimer: ReturnType<typeof setInterval> | undefined

onMounted(() => {
  measure()
  document.fonts?.ready.then(measure)
  window.addEventListener('resize', measure)
  startTimer = setTimeout(() => {
    rotateTimer = setInterval(() => {
      index.value = (index.value + 1) % props.words.length
      measure()
    }, props.interval)
  }, props.delay)
})

onBeforeUnmount(() => {
  clearTimeout(startTimer)
  clearInterval(rotateTimer)
  window.removeEventListener('resize', measure)
})
</script>

<template>
  <span
    class="relative inline-block h-[1lh] overflow-hidden align-bottom transition-[width] duration-500 ease-out motion-reduce:transition-none"
    :style="{ width }"
  >
    <span class="sr-only">{{ words.join(' or ') }}</span>
    <Transition
      enter-active-class="transition duration-500 ease-out motion-reduce:transition-none"
      enter-from-class="translate-y-full opacity-0"
      leave-active-class="absolute inset-x-0 top-0 transition duration-500 ease-in motion-reduce:transition-none"
      leave-to-class="-translate-y-full opacity-0"
    >
      <span :key="words[index]" class="block whitespace-nowrap" aria-hidden="true">{{ words[index] }}</span>
    </Transition>
    <span ref="sizes" class="invisible absolute top-0 left-0" aria-hidden="true">
      <span v-for="word in words" :key="word" class="absolute whitespace-nowrap">{{ word }}</span>
    </span>
  </span>
</template>
