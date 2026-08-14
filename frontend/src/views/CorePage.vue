<template>
  <div
    class="h-full overflow-y-auto"
    :style="padding"
  >
    <div class="w-full max-w-3xl p-3 md:px-8 md:py-6">
      <CoreSettings />
    </div>
  </div>
</template>

<script setup lang="ts">
import CoreSettings from '@/components/settings/core/CoreSettings.vue'
import { usePaddingForViews } from '@/composables/paddingViews'

const { padding } = usePaddingForViews({
  offsetTop: 0,
  offsetBottom: 8,
})
</script>
