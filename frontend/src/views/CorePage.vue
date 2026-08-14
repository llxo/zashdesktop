<template>
  <div class="relative flex h-full min-h-0 flex-col">
    <CtrlsBar>
      <div class="flex items-center gap-2 p-2">
        <SegmentedControl
          :model-value="coreType"
          :options="coreTypeOptions"
          @update:model-value="changeCoreType"
        />
      </div>
    </CtrlsBar>

    <div
      class="min-h-0 flex-1 overflow-y-auto"
      :style="padding"
    >
      <div class="w-full max-w-3xl p-3 md:px-8 md:py-6">
        <CoreSettings
          :core-type="coreType"
          @update:core-type="coreType = $event"
        />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import * as CoreService from '../../bindings/sing-box-gui/coreservice'
import CtrlsBar from '@/components/common/CtrlsBar.vue'
import SegmentedControl, { type SegmentOption } from '@/components/common/SegmentedControl.vue'
import CoreSettings from '@/components/settings/core/CoreSettings.vue'
import { usePaddingForViews } from '@/composables/paddingViews'
import { showNotification } from '@/helper/notification'
import { ref } from 'vue'

type CoreType = 'singbox' | 'mihomo'

const coreTypeOptions: SegmentOption[] = [
  { value: 'singbox', label: 'sing-box' },
  { value: 'mihomo', label: 'mihomo' },
]

const coreType = ref<CoreType>('singbox')

const changeCoreType = async (nextType: string) => {
  if (nextType === coreType.value) return
  try {
    const config = await CoreService.SaveCoreType(nextType)
    coreType.value = config.coreType === 'mihomo' ? 'mihomo' : 'singbox'
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  }
}

const { padding } = usePaddingForViews({
  offsetTop: 0,
  offsetBottom: 8,
})
</script>
