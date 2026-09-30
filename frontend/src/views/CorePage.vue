<template>
  <div
    class="relative h-full overflow-y-scroll"
    :style="padding"
  >
    <CtrlsBar>
      <div class="mx-auto flex w-full max-w-3xl items-center gap-2 p-2">
        <SegmentedControl
          :model-value="activeTab"
          :options="tabOptions"
          @update:model-value="changeTab"
        />
      </div>
    </CtrlsBar>

    <div class="mx-auto w-full max-w-3xl p-3 md:p-6">
      <CoreSettings
        v-show="activeTab !== 'settings'"
        :core-type="coreType"
      />
      <CoreGeneralSettings
        v-show="activeTab === 'settings'"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import * as CoreService from '@/../bindings/zashdesktop/coreservice'
import CtrlsBar from '@/components/common/CtrlsBar.vue'
import SegmentedControl, { type SegmentOption } from '@/components/common/SegmentedControl.vue'
import CoreGeneralSettings from '@/components/settings/core/CoreGeneralSettings.vue'
import CoreSettings from '@/components/settings/core/CoreSettings.vue'
import { usePaddingForViews } from '@/composables/paddingViews'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

type CoreType = 'sing-box' | 'mihomo'
type CoreTab = 'sing-box' | 'mihomo' | 'settings'

let lastActiveTab: CoreTab = 'sing-box'
let lastCoreType: CoreType = 'sing-box'
let initialCoreLoaded = false

const { t } = useI18n()

const activeTab = ref<CoreTab>(lastActiveTab)
const coreType = ref<CoreType>(lastCoreType)

const tabOptions = computed<SegmentOption[]>(() => [
  { value: 'sing-box', label: 'sing-box' },
  { value: 'mihomo', label: 'mihomo' },
  { value: 'settings', label: t('settings') },
])

const changeTab = (nextTab: string) => {
  if (nextTab === activeTab.value) return
  activeTab.value = nextTab as CoreTab
  lastActiveTab = activeTab.value
  if (nextTab === 'settings') {
    return
  }
  coreType.value = nextTab as CoreType
  lastCoreType = coreType.value
}

const { padding } = usePaddingForViews({
  offsetTop: 0,
  offsetBottom: 8,
})

onMounted(async () => {
  if (initialCoreLoaded) return
  try {
    const active = await CoreService.GetCoreState()
    if (active?.coreType) {
      const nextCoreType: CoreType = active.coreType === 'mihomo' ? 'mihomo' : 'sing-box'
      coreType.value = nextCoreType
      lastCoreType = nextCoreType
      if (activeTab.value !== 'settings') {
        activeTab.value = nextCoreType
        lastActiveTab = nextCoreType
      }
      initialCoreLoaded = true
    }
  } catch {}
})
</script>
