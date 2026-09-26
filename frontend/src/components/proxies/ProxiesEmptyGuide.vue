<template>
  <!-- 加载中状态 -->
  <div
    v-if="isConnecting"
    class="flex flex-col items-center justify-center p-6 text-center"
    style="min-height: 50vh;"
  >
    <div class="flex flex-col items-center gap-3">
      <span class="loading loading-spinner loading-md text-primary"></span>
      <span class="text-base-content/60 text-xs">
        {{ $t('backendConnecting') }}
      </span>
    </div>
  </div>

  <!-- 空状态引导 -->
  <div
    v-else
    class="flex flex-col items-center justify-center p-6 text-center"
    style="min-height: 50vh;"
  >
    <div
      class="border-base-border bg-base-100/70 flex max-w-md flex-col items-center gap-3 rounded-2xl border p-6 shadow-sm backdrop-blur"
    >
      <div
        class="bg-base-200 text-base-content/70 flex h-12 w-12 items-center justify-center rounded-xl"
      >
        <ServerIcon class="h-6 w-6" />
      </div>

      <div class="flex flex-col gap-1">
        <h3 class="text-base font-semibold">
          {{ $t('noProxies') }}
        </h3>
        <p class="text-base-content/60 text-xs leading-relaxed">
          {{ $t('noProxiesDesc') }}
        </p>
      </div>

      <div class="mt-2 flex flex-wrap items-center justify-center gap-2">
        <button
          class="btn btn-primary btn-sm"
          @click="handleConfigureBackend"
        >
          {{ $t('editBackendTitle') }}
        </button>
        <button
          class="btn btn-neutral btn-sm"
          @click="handleGoToCore"
        >
          {{ $t('coreSettings') }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { backendProbe } from '@/assembly/version'
import { ROUTE_NAME } from '@/constant'
import { activeUuid, openBackendManager } from '@/store/setup'
import { ServerIcon } from '@heroicons/vue/24/outline'
import { computed } from 'vue'
import { useRouter } from 'vue-router'

const router = useRouter()

const isConnecting = computed(() => {
  return backendProbe.value?.status === 'connecting'
})

const handleConfigureBackend = () => {
  if (activeUuid.value) {
    openBackendManager({ mode: 'edit', uuid: activeUuid.value })
  } else {
    openBackendManager({ mode: 'create' })
  }
}

const handleGoToCore = () => {
  router.push({ name: ROUTE_NAME.core })
}
</script>
