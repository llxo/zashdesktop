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
            @click="handleManualCheckUpdate"
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
            'pointer-events-none opacity-60': isSavingChannel || isDownloading,
          }"
        >
          <SegmentedControl
            :model-value="currentChannel"
            :options="channelOptions"
            @update:model-value="saveChannel"
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
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
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
  return props.config.installedVersion || props.config.version || t('coreInstalled')
})

const isSavingChannel = ref(false)
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
  debounceCheckUpdate(false, true, 400)
}

const saveChannel = async (rawChannel: string) => {
  if (isSavingChannel.value || rawChannel === currentChannel.value) return
  isSavingChannel.value = true
  try {
    const updated = await CoreService.UpdateCoreSettings({
      coreType: props.coreType,
      channel: rawChannel,
    })
    if (updated) {
      emit('update:config', updated)
      props.config.latestVersion = ''
      props.config.updateAvailable = false
      debounceCheckUpdate(false, true, 200)
    }
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  } finally {
    isSavingChannel.value = false
  }
}

let activeCheckKey = ''
let checkSequence = 0
let checkUpdateTimer: ReturnType<typeof setTimeout> | undefined
let lastManualCheckTime = 0

const handleManualCheckUpdate = () => {
  const now = Date.now()
  if (now - lastManualCheckTime < 800 || isChecking.value) return
  lastManualCheckTime = now
  void checkUpdate(true, true)
}

const debounceCheckUpdate = (notifyError = false, force = false, delay = 400) => {
  if (checkUpdateTimer) {
    clearTimeout(checkUpdateTimer)
  }
  checkUpdateTimer = setTimeout(() => {
    checkUpdateTimer = undefined
    void checkUpdate(notifyError, force)
  }, delay)
}

const checkUpdate = async (notifyError = true, force = false) => {
  const targetCoreType = props.coreType
  const targetChannel = currentChannel.value
  const checkKey = `${targetCoreType}:${targetChannel}`

  if (isChecking.value && activeCheckKey === checkKey && !force) return null
  activeCheckKey = checkKey
  const sequence = ++checkSequence
  isChecking.value = true
  try {
    const downloadURL = currentDownloadURL.value
    const next = force
      ? await CoreService.ForceCheckUpdate(downloadURL, targetCoreType)
      : await CoreService.CheckUpdate(downloadURL, targetCoreType)
    if (
      sequence === checkSequence &&
      props.coreType === targetCoreType &&
      currentChannel.value === targetChannel
    ) {
      props.config.latestVersion = next.latestVersion
      props.config.updateAvailable = next.updateAvailable
      if (next.installedVersion) {
        props.config.installedVersion = next.installedVersion
      }
      if (next.version) {
        props.config.version = next.version
      }
      emit('update:config', { ...props.config })
    }
    return next
  } catch (error) {
    if (
      notifyError &&
      sequence === checkSequence &&
      props.coreType === targetCoreType &&
      currentChannel.value === targetChannel
    ) {
      showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
    }
    return null
  } finally {
    if (sequence === checkSequence) {
      isChecking.value = false
      activeCheckKey = ''
    }
  }
}

const downloadCore = async () => {
  if (isDownloading.value) return
  isDownloading.value = true
  let shouldResetLoading = true
  try {
    await CoreService.DownloadCore(currentDownloadURL.value, props.coreType)
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
          const updated = await CoreService.UpdateCoreSettings({ githubProxy: true })
          if (updated) {
            emit('update:config', updated)
            props.config.githubProxy = true
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
    checkSequence += 1
    activeCheckKey = ''
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

watch(
  () => [props.coreType, props.config.channel] as const,
  ([nextType, nextChannel], [prevType, prevChannel]) => {
    if (!nextChannel) return

    const typeChanged = nextType !== prevType
    const channelChanged = nextChannel !== prevChannel

    if (typeChanged || channelChanged) {
      if (!props.config.latestVersion || channelChanged) {
        debounceCheckUpdate(false, false, 300)
      }
    }
  },
  { immediate: true },
)

onUnmounted(() => {
  if (checkUpdateTimer) {
    clearTimeout(checkUpdateTimer)
    checkUpdateTimer = undefined
  }
})

defineExpose({
  checkUpdate,
  debounceCheckUpdate,
})
</script>
