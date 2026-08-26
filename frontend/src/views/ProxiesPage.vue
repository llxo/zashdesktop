<template>
  <div
    class="relative flex size-full overflow-hidden"
    :class="[disableProxiesPageTextSelect ? 'select-none' : '']"
  >
    <FolderManagerPanel v-if="foldersUiVisible && folderManagerOpen" />
    <div
      class="max-md:scrollbar-hidden relative h-full min-w-0 flex-1"
      :class="disableProxiesPageScroll ? 'overflow-y-hidden' : 'overflow-y-scroll'"
      :style="padding"
      ref="proxiesRef"
      @scroll.passive="handleScroll"
    >
      <ProxiesCtrl />
      <FolderTopBar v-if="foldersUiVisible" />
      <!-- 空状态与离线快捷引导 -->
      <div
        v-if="renderPageItems.length === 0"
        class="flex flex-col items-center justify-center p-6 text-center"
        style="min-height: 50vh;"
      >
        <div class="border-base-border bg-base-100/70 flex max-w-md flex-col items-center gap-3 rounded-2xl border p-6 shadow-sm backdrop-blur">
          <div class="bg-base-200 text-base-content/70 flex h-12 w-12 items-center justify-center rounded-xl">
            <ServerIcon class="h-6 w-6" />
          </div>

          <div class="flex flex-col gap-1">
            <h3 class="text-base font-semibold">
              {{ isBackendOffline ? $t('backendDisconnected') : $t('noProxies') }}
            </h3>
            <p class="text-base-content/60 text-xs leading-relaxed">
              {{ isBackendOffline ? $t('backendDisconnectedDesc', { url: activeBackendProbeUrl }) : $t('noProxiesDesc') }}
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
            <button
              v-if="isBackendOffline"
              class="btn btn-ghost btn-sm"
              @click="handleRetry"
            >
              {{ $t('retry') }}
            </button>
          </div>
        </div>
      </div>
      <template v-else-if="displayTwoColumns">
        <div class="grid grid-cols-2 gap-3 p-3 md:pr-2">
          <div
            v-for="idx in [0, 1]"
            :key="idx"
            class="flex flex-1 flex-col gap-3"
          >
            <component
              v-for="name in filterContent(renderPageItems, idx)"
              :is="renderComponent"
              :key="name"
              :name="name"
            />
          </div>
        </div>
      </template>
      <div
        class="grid grid-cols-1 gap-3 p-3 md:pr-2"
        v-else
      >
        <component
          v-for="name in renderPageItems"
          :is="renderComponent"
          :key="name"
          :name="name"
        />
      </div>
    </div>
    <ProxyGroupChainModal />
  </div>
</template>

<script setup lang="ts">
import { fetchProxies, proxiesTabShow } from '@/assembly/proxies'
import { startBackendSession } from '@/assembly/session'
import { backendProbe } from '@/assembly/version'
import ProxiesCtrl from '@/components/controls/ProxiesCtrl'
import FolderManagerPanel from '@/components/proxies/folders/FolderManagerPanel.vue'
import FolderTopBar from '@/components/proxies/folders/FolderTopBar.vue'
import ProxyGroup from '@/components/proxies/ProxyGroup.vue'
import ProxyGroupChainModal from '@/components/proxies/ProxyGroupChainModal.vue'
import ProxyGroupForMobile from '@/components/proxies/ProxyGroupForMobile.vue'
import ProxyProvider from '@/components/proxies/ProxyProvider.vue'
import { usePaddingForViews } from '@/composables/paddingViews'
import {
  disableProxiesPageScroll,
  isProxiesPageMounted,
  renderProxiesPageItems,
} from '@/composables/proxies'
import { PROXY_TAB_TYPE, ROUTE_NAME } from '@/constant'
import { getBackendProbeUrl, isMiddleScreen } from '@/helper/utils'
import { folderManagerOpen, isProxyFolderModeActive } from '@/store/proxyFolders'
import { disableProxiesPageTextSelect, twoColumnProxyGroup } from '@/store/settings'
import { activeBackend, activeUuid, openBackendManager } from '@/store/setup'
import { ServerIcon } from '@heroicons/vue/24/outline'
import { useSessionStorage } from '@vueuse/core'
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'

const { padding } = usePaddingForViews({
  offsetTop: 0,
  offsetBottom: 0,
})
const renderPageItems = renderProxiesPageItems
const proxiesRef = ref()
const scrollStatus = useSessionStorage('cache/proxies-scroll-status', {
  [PROXY_TAB_TYPE.PROVIDER]: 0,
  [PROXY_TAB_TYPE.PROXIES]: 0,
})

const router = useRouter()

const isBackendOffline = computed(() => {
  if (!activeBackend.value) return true
  const probe = backendProbe.value
  if (probe?.uuid === activeUuid.value && probe.status === 'failed') return true
  return false
})

const activeBackendProbeUrl = computed(() =>
  activeBackend.value ? getBackendProbeUrl(activeBackend.value) : '',
)

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

const handleRetry = () => {
  startBackendSession()
}

const handleScroll = () => {
  if (!proxiesRef.value) return
  scrollStatus.value[proxiesTabShow.value] = proxiesRef.value.scrollTop
}

const waitTickUntilReady = (startTime = performance.now()) => {
  const proxiesEl = proxiesRef.value
  const isTimedOut = performance.now() - startTime > 300

  if (
    isTimedOut ||
    (proxiesEl && proxiesEl.scrollHeight > scrollStatus.value[proxiesTabShow.value])
  ) {
    if (!proxiesEl) return
    proxiesEl.scrollTo({
      top: scrollStatus.value[proxiesTabShow.value],
      behavior: 'smooth',
    })
  } else {
    requestAnimationFrame(() => {
      waitTickUntilReady(startTime)
    })
  }
}

watch(proxiesTabShow, () =>
  nextTick(() => {
    waitTickUntilReady()
  }),
)

isProxiesPageMounted.value = false

onMounted(() => {
  setTimeout(() => {
    isProxiesPageMounted.value = true
    nextTick(() => {
      waitTickUntilReady()
      fetchProxies()
    })
  })
})

const renderComponent = computed(() => {
  if (proxiesTabShow.value === PROXY_TAB_TYPE.PROVIDER) {
    return ProxyProvider
  }

  if (isMiddleScreen.value && displayTwoColumns.value) {
    return ProxyGroupForMobile
  }

  return ProxyGroup
})

const foldersUiVisible = computed(
  () => isProxyFolderModeActive.value && proxiesTabShow.value === PROXY_TAB_TYPE.PROXIES,
)

const displayTwoColumns = computed(() => {
  if (proxiesTabShow.value === PROXY_TAB_TYPE.PROVIDER && isMiddleScreen.value) {
    return false
  }
  return twoColumnProxyGroup.value && renderPageItems.value.length > 1
})

const filterContent: <T>(all: T[], target: number) => T[] = (all, target) => {
  return all.filter((_, index: number) => index % 2 === target)
}
</script>
