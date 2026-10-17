<script setup lang="ts">
interface Chip {
  one: string
  two: string
  iconOne: string
  iconTwo: string
}

interface Ring {
  radius: number
  startAngle: number
  seconds: number
  tone: 'inner' | 'middle' | 'outer'
  chips: Chip[]
}

// Icon names are literals, so the icon client bundle includes them.
const rings: Ring[] = [
  {
    radius: 0.188,
    startAngle: -30,
    seconds: 34,
    tone: 'inner',
    chips: [
      { one: 'maven', two: 'gradle', iconOne: 'i-simple-icons-apachemaven', iconTwo: 'i-simple-icons-gradle' },
      { one: 'npm', two: 'pnpm', iconOne: 'i-simple-icons-npm', iconTwo: 'i-simple-icons-pnpm' },
      { one: 'docker', two: 'podman', iconOne: 'i-simple-icons-docker', iconTwo: 'i-simple-icons-podman' }
    ]
  },
  {
    radius: 0.303,
    startAngle: 18,
    seconds: 52,
    tone: 'middle',
    chips: [
      { one: 'pypi', two: 'poetry', iconOne: 'i-simple-icons-python', iconTwo: 'i-simple-icons-poetry' },
      { one: 'go', two: 'goproxy', iconOne: 'i-simple-icons-go', iconTwo: 'i-simple-icons-go' },
      { one: 'helm', two: 'oci', iconOne: 'i-simple-icons-helm', iconTwo: 'i-simple-icons-helm' },
      { one: 'cargo', two: 'crates', iconOne: 'i-simple-icons-rust', iconTwo: 'i-simple-icons-rust' },
      { one: 'nuget', two: 'dotnet', iconOne: 'i-simple-icons-nuget', iconTwo: 'i-simple-icons-dotnet' }
    ]
  },
  {
    radius: 0.418,
    startAngle: 0,
    seconds: 74,
    tone: 'outer',
    chips: [
      { one: 'terraform', two: 'opentofu', iconOne: 'i-simple-icons-terraform', iconTwo: 'i-simple-icons-opentofu' },
      { one: 'gems', two: 'bundler', iconOne: 'i-simple-icons-rubygems', iconTwo: 'i-simple-icons-rubygems' },
      { one: 'conan', two: 'conan 2', iconOne: 'i-simple-icons-conan', iconTwo: 'i-simple-icons-conan' },
      { one: 'huggingface', two: 'datasets', iconOne: 'i-simple-icons-huggingface', iconTwo: 'i-simple-icons-huggingface' },
      { one: 'nix', two: 'nixpkgs', iconOne: 'i-simple-icons-nixos', iconTwo: 'i-simple-icons-nixos' },
      { one: 'debian', two: 'apt', iconOne: 'i-simple-icons-debian', iconTwo: 'i-simple-icons-debian' },
      { one: 'rpm', two: 'dnf', iconOne: 'i-simple-icons-redhat', iconTwo: 'i-simple-icons-redhat' },
      { one: 'alpine', two: 'apk', iconOne: 'i-simple-icons-alpinelinux', iconTwo: 'i-simple-icons-alpinelinux' }
    ]
  }
]

const circles = [
  { size: 0.364, class: 'border-default' },
  { size: 0.594, class: 'border-muted' },
  { size: 0.824, class: 'border-muted/60' }
]

const tones = {
  inner: { size: 'w-35 h-10', face: 'text-sm bg-elevated text-highlighted ring-accented', icon: 'text-primary' },
  middle: { size: 'w-35 h-10', face: 'text-sm bg-elevated/70 text-default ring-default', icon: 'text-toned' },
  outer: { size: 'w-34 h-9.5', face: 'text-xs bg-elevated/40 text-muted ring-default', icon: 'text-muted' }
}

function placement(ring: Ring, index: number) {
  const angle = ring.startAngle + index * 360 / ring.chips.length
  return {
    transform: `translate(-50%, -50%) rotate(${angle}deg) translateX(calc(var(--orbit) * ${ring.radius})) rotate(${-angle}deg)`
  }
}

const delay = (index: number) => `${(-1.7 * index).toFixed(1)}s`
</script>

<template>
  <div class="relative mx-auto aspect-square w-(--orbit) [--orbit:34rem] xl:[--orbit:41rem]" aria-hidden="true">
    <div
      v-for="circle in circles"
      :key="circle.size"
      class="absolute top-1/2 left-1/2 -translate-1/2 rounded-full border"
      :class="circle.class"
      :style="{ width: `calc(var(--orbit) * ${circle.size})`, height: `calc(var(--orbit) * ${circle.size})` }"
    />

    <div class="absolute top-1/2 left-1/2 -translate-1/2 flex size-34 items-center justify-center rounded-full bg-default ring ring-primary/35 shadow-[0_0_60px_color-mix(in_oklab,var(--ui-primary)_18%,transparent)]">
      <UIcon name="i-lucide-package" class="size-16 text-primary" />
    </div>

    <div
      v-for="ring in rings"
      :key="ring.tone"
      class="absolute inset-0 animate-orbit-spin motion-reduce:animate-none"
      :style="{ animationDuration: `${ring.seconds}s` }"
    >
      <div
        v-for="(chip, index) in ring.chips"
        :key="chip.one"
        class="absolute top-1/2 left-1/2"
        :style="placement(ring, index)"
      >
        <div class="animate-orbit-spin-back motion-reduce:animate-none" :style="{ animationDuration: `${ring.seconds}s` }">
          <div class="relative animate-orbit-breathe motion-reduce:animate-none" :class="tones[ring.tone].size" :style="{ animationDelay: delay(index) }">
            <span
              v-for="(face, side) in [{ name: chip.one, icon: chip.iconOne }, { name: chip.two, icon: chip.iconTwo }]"
              :key="side"
              class="absolute inset-0 flex items-center justify-center gap-2 rounded-sm font-mono ring"
              :class="[tones[ring.tone].face, side === 0 ? 'animate-orbit-face-one motion-reduce:animate-none' : 'animate-orbit-face-two motion-reduce:animate-none motion-reduce:opacity-0']"
              :style="{ animationDelay: delay(index) }"
            >
              <UIcon :name="face.icon" class="size-4 shrink-0" :class="tones[ring.tone].icon" />
              <!-- The name is generated content, not text: the chips are decoration (WCAG 1.4.3). -->
              <span :data-name="face.name" class="before:content-[attr(data-name)]" />
            </span>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
