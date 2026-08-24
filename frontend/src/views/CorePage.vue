<template>
  <div class="relative flex h-full min-h-0 flex-col">
    <CtrlsBar>
      <div class="flex items-center gap-2 p-2">
        <button
          v-if="!activeBackend"
          class="btn btn-ghost btn-sm"
          @click="router.push({ name: ROUTE_NAME.setup })"
        >
          <ArrowLeftIcon class="h-4 w-4" />
          {{ $t('setup') }}
        </button>
        <SegmentedControl
          :model-value="activeTab"
          :options="tabOptions"
          @update:model-value="changeTab"
        />
      </div>
    </CtrlsBar>

    <div
      class="min-h-0 flex-1 overflow-y-auto"
      :style="padding"
    >
      <div class="w-full max-w-3xl p-3 md:px-8 md:py-6">
        <CoreSettings
          :active-tab="activeTab"
          :core-type="coreType"
          @update:core-type="handleCoreTypeUpdate"
        />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import * as CoreService from '../../bindings/zashdesktop/coreservice'
import CtrlsBar from '@/components/common/CtrlsBar.vue'
import SegmentedControl, { type SegmentOption } from '@/components/common/SegmentedControl.vue'
import CoreSettings from '@/components/settings/core/CoreSettings.vue'
import { usePaddingForViews } from '@/composables/paddingViews'
import { showNotification } from '@/helper/notification'
import { ROUTE_NAME } from '@/constant'
import router from '@/router'
import { activeBackend } from '@/store/setup'
import { ArrowLeftIcon } from '@heroicons/vue/24/outline'
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'

type CoreType = 'sing-box' | 'mihomo'
type CoreTab = 'sing-box' | 'mihomo' | 'settings'

const { t } = useI18n()

const activeTab = ref<CoreTab>('sing-box')
const coreType = ref<CoreType>('sing-box')

const tabOptions = computed<SegmentOption[]>(() => [
  { value: 'sing-box', label: 'sing-box' },
  { value: 'mihomo', label: 'mihomo' },
  { value: 'settings', label: t('settings') },
])

const handleCoreTypeUpdate = (nextType: CoreType) => {
  coreType.value = nextType
  if (activeTab.value !== 'settings') {
    activeTab.value = nextType
  }
}

const changeTab = async (nextTab: string) => {
  if (nextTab === activeTab.value) return
  if (nextTab === 'settings') {
    activeTab.value = 'settings'
    return
  }
  const nextCoreType: CoreType = nextTab === 'mihomo' ? 'mihomo' : 'sing-box'
  try {
    const config = await CoreService.SaveCoreType(nextCoreType)
    coreType.value = config.coreType === 'mihomo' ? 'mihomo' : 'sing-box'
    activeTab.value = coreType.value
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  }
}

const { padding } = usePaddingForViews({
  offsetTop: 0,
  offsetBottom: 8,
})
</script>
