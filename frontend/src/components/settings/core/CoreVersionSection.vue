<template>
  <!-- 下载核心 -->
  <section>
    <div class="text-base-content/85 mt-1 mb-2.5 px-1 text-base font-semibold tracking-tight">
      {{ $t('coreMaintenance') }}
    </div>
    <div class="settings-grid">
      <!-- 核心版本 -->
      <div class="setting-item">
        <span class="w-20 sm:w-24 shrink-0 text-sm font-medium whitespace-nowrap">
          {{ $t('coreVersion') }}
        </span>
        <div class="flex flex-1 items-center justify-end gap-2.5">
          <span
            class="badge badge-sm badge-ghost font-mono whitespace-nowrap"
            :class="{ 'opacity-60': !config.installed }"
          >
            {{ installedVersionLabel }}
          </span>
          <template v-if="config.latestVersion">
            <template v-if="config.updateAvailable">
              <ArrowRightIcon class="text-base-content/40 h-3.5 w-3.5 shrink-0" />
              <span class="badge badge-warning badge-sm font-mono whitespace-nowrap">
                {{ config.latestVersion }}
              </span>
            </template>
            <span
              v-else-if="config.installed"
              class="text-success/90 inline-flex items-center gap-1.5 text-xs font-medium whitespace-nowrap"
            >
              <span class="bg-success inline-block h-1.5 w-1.5 rounded-full"></span>
              <span>{{ $t('upToDate') }}</span>
            </span>
          </template>
          <button
            class="btn btn-circle btn-ghost btn-xs shrink-0"
            type="button"
            :aria-label="$t('checkUpdate')"
            :title="$t('checkUpdate')"
            :disabled="isChecking || isDownloading"
            @click="checkUpdate"
          >
            <span
              v-if="isChecking"
              class="loading loading-spinner h-3.5 w-3.5"
            ></span>
            <ArrowPathIcon
              v-else
              class="h-3.5 w-3.5"
            />
          </button>
        </div>
      </div>

      <!-- 渠道切换 -->
      <div class="setting-item">
        <span class="w-20 sm:w-24 shrink-0 text-sm font-medium whitespace-nowrap">
          {{ $t('coreChannel') }}
        </span>
        <div
          class="flex flex-1 justify-end"
          :class="{
            'pointer-events-none opacity-60': isSwitchingChannel || isDownloading,
          }"
        >
          <SegmentedControl
            :model-value="currentChannel"
            :options="channelOptions"
            @update:model-value="switchChannel"
          />
        </div>
      </div>

      <!-- 下载源与核心更新 -->
      <div class="setting-item">
        <span class="w-20 sm:w-24 shrink-0 text-sm font-medium whitespace-nowrap">
          {{ $t('coreDownloadSource') }}
        </span>
        <div class="join flex-1 min-w-0">
          <select
            v-model="selectedSourceLabel"
            class="join-item select select-sm flex-1 min-w-0"
            :aria-label="$t('coreDownloadSource')"
            :disabled="isDownloading"
            @change="handleSourceChange"
          >
            <option
              v-for="source in sourceOptions"
              :key="source.label"
              :value="source.label"
            >
              {{ source.label }}
            </option>
          </select>
          <button
            class="join-item btn btn-sm shrink-0 whitespace-nowrap"
            :class="{ 'btn-primary': !config.installed || config.updateAvailable }"
            type="button"
            :disabled="isDownloading"
            @click="downloadCore"
          >
            <span
              v-if="isDownloading"
              class="loading loading-spinner h-4 w-4"
            ></span>
            <ArrowDownCircleIcon
              v-else
              class="h-4 w-4"
            />
            {{ $t('updateCore') }}
          </button>
        </div>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import * as CoreService from '../../../../bindings/zashdesktop/coreservice'
import type { CoreConfig } from '../../../../bindings/zashdesktop/models'
import SegmentedControl, { type SegmentOption } from '@/components/common/SegmentedControl.vue'
import { showConfirmDialog } from '@/helper/confirmDialog'
import { showNotification } from '@/helper/notification'
import {
  ArrowDownCircleIcon,
  ArrowPathIcon,
  ArrowRightIcon,
} from '@heroicons/vue/24/outline'
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  builtInDownloadSources,
  type CoreChannel,
  type CoreType,
  type DownloadSource,
} from './coreSources'

const props = defineProps<{
  coreType: CoreType
  config: CoreConfig
}>()

const emit = defineEmits<{
  (e: 'update:config', config: CoreConfig): void
}>()

const { t } = useI18n()

const channelOptions = computed<SegmentOption[]>(() => [
  { value: 'stable', label: t('coreStableBuild') },
  { value: 'test', label: t('coreTestBuild') },
])

const currentChannel = computed<CoreChannel>(() => (props.config.channel === 'test' ? 'test' : 'stable'))

const installedVersionLabel = computed(() => {
  if (!props.config.installed) return t('coreNotInstalled')
  return props.config.version || t('coreInstalled')
})

const isChecking = ref(false)
const isDownloading = ref(false)

const sourceOptions = computed(() => builtInDownloadSources[props.coreType])
const sourceURL = (source: DownloadSource, channel = currentChannel.value) =>
  source.channelURLs?.[channel] ?? source.url

const sourceStorageKey = computed(() => `core-download-source:${props.coreType}`)
const selectedSourceLabel = ref('')

const loadSavedSource = () => {
  try {
    const saved = localStorage.getItem(sourceStorageKey.value)
    if (saved && sourceOptions.value.some((s) => s.label === saved)) {
      selectedSourceLabel.value = saved
      return
    }
  } catch {}
  selectedSourceLabel.value = sourceOptions.value[0]?.label || ''
}

const currentSource = computed(() => {
  return (
    sourceOptions.value.find((s) => s.label === selectedSourceLabel.value) ||
    sourceOptions.value[0]
  )
})

const currentDownloadURL = computed(() => {
  if (!currentSource.value) return ''
  return sourceURL(currentSource.value, currentChannel.value)
})

const handleSourceChange = () => {
  try {
    localStorage.setItem(sourceStorageKey.value, selectedSourceLabel.value)
  } catch {}
}

const isSwitchingChannel = ref(false)

const switchChannel = async (rawChannel: string) => {
  if (isSwitchingChannel.value || rawChannel === currentChannel.value) return
  isSwitchingChannel.value = true
  try {
    const updated = await CoreService.UpdateCoreSettings({
      coreType: props.coreType,
      channel: rawChannel,
    })
    if (updated) {
      emit('update:config', {
        ...props.config,
        ...updated,
        latestVersion: '',
        updateAvailable: false,
      })
    }
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  } finally {
    isSwitchingChannel.value = false
  }
}

let lastCheckTime = 0

const checkUpdate = async () => {
  const now = Date.now()
  if (now - lastCheckTime < 600 || isChecking.value) return
  lastCheckTime = now

  const targetCoreType = props.coreType
  const targetChannel = currentChannel.value

  isChecking.value = true
  try {
    const next = await CoreService.ForceCheckUpdate(currentDownloadURL.value, targetCoreType)
    if (
      next &&
      props.coreType === targetCoreType &&
      currentChannel.value === targetChannel
    ) {
      emit('update:config', {
        ...props.config,
        latestVersion: next.latestVersion,
        updateAvailable: next.updateAvailable,
        ...(next.version ? { version: next.version } : {}),
      })
    }
  } catch (error) {
    if (
      props.coreType === targetCoreType &&
      currentChannel.value === targetChannel
    ) {
      showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
    }
  } finally {
    if (
      props.coreType === targetCoreType &&
      currentChannel.value === targetChannel
    ) {
      isChecking.value = false
    }
  }
}

const downloadCore = async () => {
  if (isDownloading.value) return
  isDownloading.value = true
  let shouldResetLoading = true
  try {
    const updated = await CoreService.DownloadCore(currentDownloadURL.value, props.coreType)
    if (updated) {
      emit('update:config', {
        ...props.config,
        ...updated,
        latestVersion: updated.latestVersion || '',
        updateAvailable: Boolean(updated.updateAvailable),
        ...(updated.version ? { version: updated.version } : {}),
        installed: updated.installed,
      })
    }
    showNotification({ content: 'coreDownloadSuccess', type: 'alert-success' })
  } catch (error) {
    const errorMsg = String(error)
    const isProxyDisabled = !props.config.githubProxy || errorMsg.includes('githubProxyDisabled')
    if (isProxyDisabled) {
      isDownloading.value = false
      const { confirmed } = await showConfirmDialog({
        title: t('githubProxyPromptTitle'),
        message: t('githubProxyPromptMessage'),
        confirmText: t('enableAndRetry'),
      })
      if (confirmed) {
        try {
          const updated = await CoreService.UpdateCoreSettings({
            coreType: props.coreType,
            githubProxy: true,
          })
          if (updated) {
            emit('update:config', {
              ...props.config,
              ...updated,
              githubProxy: true,
            })
          }
          shouldResetLoading = false
          await downloadCore()
          return
        } catch (saveErr) {
          showNotification({ content: String(saveErr), type: 'alert-error', timeout: 0 })
        }
      }
      return
    }
    showNotification({ content: errorMsg, type: 'alert-error', timeout: 0 })
  } finally {
    if (shouldResetLoading) {
      isDownloading.value = false
    }
  }
}

watch(
  () => props.coreType,
  () => {
    isChecking.value = false
    loadSavedSource()
  },
  { immediate: true },
)

watch(
  sourceOptions,
  (options) => {
    if (!options.some((s) => s.label === selectedSourceLabel.value)) {
      loadSavedSource()
    }
  },
)

defineExpose({
  checkUpdate,
})
</script>
