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
import { ref } from 'vue'

type CoreType = 'sing-box' | 'mihomo'

const coreTypeOptions: SegmentOption[] = [
  { value: 'sing-box', label: 'sing-box' },
  { value: 'mihomo', label: 'mihomo' },
]

const coreType = ref<CoreType>('sing-box')

const changeCoreType = async (nextType: string) => {
  if (nextType === coreType.value) return
  try {
    const config = await CoreService.SaveCoreType(nextType)
    coreType.value = config.coreType === 'mihomo' ? 'mihomo' : 'sing-box'
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  }
}

const { padding } = usePaddingForViews({
  offsetTop: 0,
  offsetBottom: 8,
})
</script>
