<script setup lang="ts">
const props = defineProps<{ repo: string, folder: string, files?: File[] }>()
const emit = defineEmits<{ close: [uploaded: string | false] }>()
const toast = useToast()

interface Upload {
  file: File
  progress: number
  state: 'queued' | 'uploading' | 'done' | 'error'
  error?: string
}

const folder = ref(props.folder)
const uploads = ref<Upload[]>([])
const busy = ref(false)
let controller: AbortController | undefined

function add(files: File[] | null | undefined) {
  for (const file of files ?? []) {
    uploads.value = uploads.value.filter(u => u.state === 'done' || u.state === 'uploading' || u.file.name !== file.name)
    uploads.value.push({ file, progress: 0, state: 'queued' })
  }
}
add(props.files)

const picked = ref<File[] | null>(null)
watch(picked, (files) => {
  if (files?.length) {
    add(files)
    picked.value = null
  }
})

const target = computed(() => folder.value.split('/').filter(Boolean).join('/'))
const folderError = computed(() =>
  target.value.split('/').some(s => s === '.' || s === '..') ? 'A folder cannot contain "." or ".." segments' : undefined
)
const pending = computed(() => uploads.value.filter(u => u.state === 'queued' || u.state === 'error'))

async function start() {
  busy.value = true
  controller = new AbortController()
  const dir = target.value
  let done = 0
  for (const u of pending.value) {
    if (controller.signal.aborted) {
      break
    }
    u.state = 'uploading'
    u.progress = 0
    u.error = undefined
    try {
      await uploadFile(props.repo, dir ? `${dir}/${u.file.name}` : u.file.name, u.file, (p) => {
        u.progress = Math.round(p * 100)
      }, controller.signal)
      u.state = 'done'
      done++
    } catch (err) {
      u.state = 'error'
      u.error = (err as Error).message
    }
  }
  busy.value = false
  if (done && !uploads.value.some(u => u.state === 'error')) {
    toast.add({ title: `${pluralize(done, 'file')} uploaded to ${props.repo}/${dir}`, color: 'success', icon: 'i-lucide-check' })
    emit('close', dir)
  }
}

function close() {
  controller?.abort()
  emit('close', uploads.value.some(u => u.state === 'done') ? target.value : false)
}

const stateIcon = {
  queued: { icon: 'i-lucide-file', class: 'text-muted' },
  uploading: { icon: 'i-lucide-loader-circle', class: 'text-primary animate-spin' },
  done: { icon: 'i-lucide-circle-check', class: 'text-success' },
  error: { icon: 'i-lucide-circle-x', class: 'text-error' }
} as const
</script>

<template>
  <USlideover
    :title="`Upload to ${repo}`"
    description="Files keep their names. An existing file is replaced only if you may delete it."
    :close="{ onClick: close }"
    :dismissible="!busy"
  >
    <template #body>
      <div class="space-y-5">
        <UFormField
          label="Folder"
          description="Inside the repository; empty for its root. Missing folders are created."
          :error="folderError"
        >
          <UInput
            v-model="folder"
            icon="i-lucide-folder"
            placeholder="org/app/1.0"
            :disabled="busy"
            class="w-full font-mono"
          />
        </UFormField>

        <UFileUpload
          v-model="picked"
          multiple
          :preview="false"
          :disabled="busy"
          icon="i-lucide-upload"
          label="Drop files here"
          description="or click to choose them"
          class="min-h-36 w-full"
        />

        <ul v-if="uploads.length" class="divide-y divide-default rounded-md border border-default">
          <li v-for="(u, i) in uploads" :key="`${u.file.name}:${i}`" class="flex items-start gap-3 p-3">
            <UIcon :name="stateIcon[u.state].icon" :class="['mt-0.5 size-5 shrink-0', stateIcon[u.state].class]" />
            <div class="min-w-0 flex-1 space-y-1.5">
              <div class="flex items-baseline justify-between gap-2">
                <span class="truncate text-sm text-highlighted">{{ u.file.name }}</span>
                <span class="shrink-0 text-xs text-muted">{{ formatBytes(u.file.size) }}</span>
              </div>
              <UProgress
                v-if="u.state === 'uploading' || u.state === 'done'"
                :model-value="u.progress"
                size="xs"
                :color="u.state === 'done' ? 'success' : 'primary'"
              />
              <p v-if="u.error" class="text-xs text-error">
                {{ u.error }}
              </p>
            </div>
            <UButton
              v-if="u.state === 'queued' || u.state === 'error'"
              icon="i-lucide-x"
              color="neutral"
              variant="ghost"
              size="xs"
              :aria-label="`Remove ${u.file.name}`"
              :disabled="busy"
              @click="uploads.splice(i, 1)"
            />
          </li>
        </ul>
      </div>
    </template>

    <template #footer>
      <UButton
        :label="busy ? 'Cancel' : 'Close'"
        color="neutral"
        variant="outline"
        @click="close"
      />
      <UButton
        icon="i-lucide-upload"
        :label="pending.length ? `Upload ${pluralize(pending.length, 'file')}` : 'Upload'"
        :loading="busy"
        :disabled="!pending.length || !!folderError"
        @click="start"
      />
    </template>
  </USlideover>
</template>
