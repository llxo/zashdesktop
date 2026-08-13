<template>
  <div
    class="h-full overflow-y-auto"
    :style="padding"
  >
    <div class="mx-auto flex w-full max-w-4xl flex-col gap-3 p-3 md:px-8 md:py-6">
      <div class="flex items-center gap-2 px-1">
        <CpuChipIcon class="h-5 w-5" />
        <h1 class="text-lg font-semibold">{{ $t('coreSettings') }}</h1>
      </div>
      <CoreSettings />
    </div>
  </div>
</template>

<script setup lang="ts">
import CoreSettings from '@/components/settings/core/CoreSettings.vue'
import { usePaddingForViews } from '@/composables/paddingViews'
import { CpuChipIcon } from '@heroicons/vue/24/outline'

const { padding } = usePaddingForViews({
  offsetTop: 0,
  offsetBottom: 8,
})
</script>
