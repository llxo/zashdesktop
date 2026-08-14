<template>
  <div class="flex flex-col gap-3 text-sm">
    <section>
      <div class="text-base-content/85 mt-1 mb-2.5 px-1 text-base font-semibold tracking-tight">
        {{ $t('coreRun') }}
      </div>
      <div class="settings-grid">
        <div class="setting-item justify-end">
          <span
            class="badge badge-sm"
            :class="config.running ? 'badge-success' : 'badge-ghost'"
          >
            {{ config.running ? $t('coreRunning') : $t('coreStopped') }}
          </span>
        </div>

        <div class="setting-item flex-col !items-stretch py-3">
          <input
            v-model="runArgsInput"
            class="input input-sm w-full font-mono text-xs"
            type="text"
            :aria-label="$t('coreRunArgs')"
            :placeholder="defaultRunArgsPlaceholder"
            :disabled="config.running || isStarting || isStopping || isRestarting"
            @input="runArgsDirty = true"
          />

          <div class="flex self-start flex-wrap gap-2">
            <button
              v-if="!config.running"
              class="btn btn-primary btn-sm"
              :disabled="isStarting || isStopping || isRestarting || !config.installed"
              @click="startCore"
            >
              <span
                v-if="isStarting"
                class="loading loading-spinner h-4 w-4"
              ></span>
              <PlayIcon
                v-else
                class="h-4 w-4"
              />
              {{ $t('coreStart') }}
            </button>
            <button
              v-else
              class="btn btn-error btn-sm"
              :disabled="isStarting || isStopping || isRestarting"
              @click="stopCore"
            >
              <span
                v-if="isStopping"
                class="loading loading-spinner h-4 w-4"
              ></span>
              <StopIcon
                v-else
                class="h-4 w-4"
              />
              {{ $t('coreStop') }}
            </button>
            <button
              class="btn btn-sm"
              :disabled="isStarting || isStopping || isRestarting || !config.installed"
              @click="restartCore"
            >
              <span
                v-if="isRestarting"
                class="loading loading-spinner h-4 w-4"
              ></span>
              <ArrowPathIcon
                v-else
                class="h-4 w-4"
              />
              {{ $t('coreRestart') }}
            </button>
            <button
              class="btn btn-ghost btn-sm"
              :disabled="isSavingRunArgs || config.running"
              @click="saveRunArgs"
            >
              <span
                v-if="isSavingRunArgs"
                class="loading loading-spinner h-4 w-4"
              ></span>
              <BookmarkSquareIcon
                v-else
                class="h-4 w-4"
              />
              {{ $t('save') }}
            </button>
          </div>
        </div>
      </div>
    </section>

    <section>
      <div class="text-base-content/85 mt-1 mb-2.5 px-1 text-base font-semibold tracking-tight">
        {{ $t('coreDownload') }}
      </div>
      <div class="settings-grid">
        <div class="setting-item flex-col !items-stretch py-3">
          <div class="flex w-full flex-wrap gap-2">
            <select
              v-model="selectedSourceURL"
              class="select select-sm min-w-44 flex-1"
              :aria-label="$t('coreDownloadURL')"
              :disabled="isSaving || isValidatingURL || isDownloading"
              @change="selectDownloadSource"
            >
              <option
                disabled
                value=""
              >
                {{ $t('coreDownloadURL') }}
              </option>
              <option
                v-for="source in sourceOptions"
                :key="source.url"
                :value="source.url"
              >
                {{ source.label }}
              </option>
            </select>
          </div>
          <input
            v-model="urlInput"
            class="input input-sm w-full"
            type="url"
            :aria-label="$t('coreDownloadURLPlaceholder')"
            :placeholder="$t('coreDownloadURLPlaceholder')"
            @change="saveURL"
            @keydown.enter.prevent="validateAndAddURL"
          />

          <div class="flex self-start flex-wrap gap-2">
            <button
              class="btn btn-primary btn-sm"
              :disabled="isSaving || isValidatingURL || isDownloading || !urlInput.trim()"
              @click="saveURL"
            >
              <span
                v-if="isSaving || isValidatingURL"
                class="loading loading-spinner h-4 w-4"
              ></span>
              <ArrowDownTrayIcon
                v-else
                class="h-4 w-4"
              />
              {{ $t('save') }}
            </button>
            <button
              class="btn btn-sm"
              :disabled="isSaving || isChecking || isDownloading || !urlInput.trim()"
              @click="checkUpdate"
            >
              <span
                v-if="isChecking"
                class="loading loading-spinner h-4 w-4"
              ></span>
              <ArrowPathIcon
                v-else
                class="h-4 w-4"
              />
              {{ $t('checkUpdate') }}
            </button>
            <button
              class="btn btn-sm"
              :disabled="isSaving || isChecking || isDownloading || !urlInput.trim()"
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
              {{ config.updateAvailable ? $t('updateCore') : $t('downloadCore') }}
            </button>
          </div>
        </div>
      </div>
    </section>

    <section>
      <div class="text-base-content/85 mt-1 mb-2.5 px-1 text-base font-semibold tracking-tight">
        {{ $t('downloadConfig') }}
      </div>
      <div class="settings-grid">
        <div class="setting-item flex-col !items-stretch py-3">
          <input
            v-model="configURLInput"
            class="input input-sm w-full"
            type="url"
            :aria-label="$t('coreConfigURLPlaceholder')"
            :placeholder="$t('coreConfigURLPlaceholder')"
          />
          <div class="flex self-start flex-wrap gap-2">
            <button
              class="btn btn-primary btn-sm"
              :disabled="isDownloadingConfig || !configURLInput.trim()"
              @click="downloadConfig"
            >
              <span
                v-if="isDownloadingConfig"
                class="loading loading-spinner h-4 w-4"
              ></span>
              <ArrowDownTrayIcon
                v-else
                class="h-4 w-4"
              />
              {{ $t('downloadConfig') }}
            </button>
          </div>
        </div>
      </div>
    </section>

    <section>
      <div class="text-base-content/85 mt-1 mb-2.5 px-1 text-base font-semibold tracking-tight">
        {{ $t('coreBehavior') }}
      </div>
      <div class="settings-grid">
        <label class="setting-item">
          <span class="setting-item-label">{{ $t('coreRunAsAdmin') }}</span>
          <input
            v-model="config.runAsAdmin"
            class="toggle"
            type="checkbox"
            :disabled="isSavingBehavior"
            @change="saveBehavior"
          />
        </label>
        <label class="setting-item">
          <span class="setting-item-label">{{ $t('coreAutoStart') }}</span>
          <input
            v-model="config.autoStart"
            class="toggle"
            type="checkbox"
            :disabled="isSavingBehavior"
            @change="saveBehavior"
          />
        </label>
        <label class="setting-item">
          <span class="setting-item-label">{{ $t('coreAutoStartCore') }}</span>
          <input
            v-model="config.autoStartCore"
            class="toggle"
            type="checkbox"
            :disabled="isSavingBehavior"
            @change="saveBehavior"
          />
        </label>
        <label class="setting-item">
          <span class="setting-item-label">{{ $t('backendDebugLog') }}</span>
          <input
            v-model="config.backendDebugLog"
            class="toggle"
            type="checkbox"
            :disabled="isSavingBehavior"
            @change="saveBehavior"
          />
        </label>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import * as CoreService from '../../../../bindings/sing-box-gui/coreservice'
import type { CoreConfig } from '../../../../bindings/sing-box-gui/models'
import { version } from '@/assembly/version'
import { showNotification } from '@/helper/notification'
import {
  ArrowDownCircleIcon,
  ArrowDownTrayIcon,
  ArrowPathIcon,
  BookmarkSquareIcon,
  PlayIcon,
  StopIcon,
} from '@heroicons/vue/24/outline'
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'

type CoreType = 'singbox' | 'mihomo'

type DownloadSource = {
  label: string
  url: string
}

const builtInDownloadSources: Record<CoreType, DownloadSource[]> = {
  singbox: [
    {
      label: 'llxo/sing-box-releases',
      url: 'https://github.com/llxo/sing-box-releases/releases/download/v{version}/sing-box-{version}-windows-amd64.zip',
    },
    {
      label: 'reF1nd/sing-box-releases',
      url: 'https://github.com/reF1nd/sing-box-releases/releases/download/v{version}/sing-box-{version}-windows-amd64.zip',
    },
    {
      label: 'SagerNet/sing-box',
      url: 'https://github.com/SagerNet/sing-box/releases/download/v{version}/sing-box-{version}-windows-amd64.zip',
    },
  ],
  mihomo: [],
}

const props = defineProps<{
  coreType: CoreType
}>()

const emit = defineEmits<{
  (event: 'update:coreType', value: CoreType): void
}>()

const config = reactive<CoreConfig>({
  coreType: 'singbox',
  urlTemplate: '',
  configuredVersion: '',
  version: '',
  versionDetail: '',
  channel: '',
  corePath: '',
  installedVersion: '',
  installed: false,
  latestVersion: '',
  updateAvailable: false,
  runArgs: '',
  configURL: '',
  running: false,
  pid: 0,
  logPath: '',
  configPath: '',
  configAvailable: false,
  runAsAdmin: false,
  autoStart: false,
  autoStartCore: false,
  backendDebugLog: false,
})
const coreType = computed(() => props.coreType)
const defaultRunArgsPlaceholder = computed(() =>
  coreType.value === 'mihomo' ? '-d .' : 'run -c config.json -D .',
)
const urlInput = ref('')
const selectedSourceURL = ref('')
const customDownloadSources = ref<DownloadSource[]>([])
const runArgsInput = ref('')
const configURLInput = ref('')
const runArgsDirty = ref(false)
const isSaving = ref(false)
const isChecking = ref(false)
const isDownloading = ref(false)
const isValidatingURL = ref(false)
const isStarting = ref(false)
const isStopping = ref(false)
const isRestarting = ref(false)
const isSavingRunArgs = ref(false)
const isDownloadingConfig = ref(false)
const isSavingBehavior = ref(false)
const isRefreshing = ref(false)
let refreshTimer: ReturnType<typeof setInterval> | undefined
const sourceStorageKey = computed(() => `core-download-sources:${coreType.value}`)
const sourceOptions = computed(() => [
  ...builtInDownloadSources[coreType.value],
  ...customDownloadSources.value,
])

const sourceLabel = (rawURL: string) => {
  try {
    const segments = new URL(rawURL).pathname.split('/').filter(Boolean)
    return segments.length >= 2 ? `${segments[0]}/${segments[1]}` : rawURL
  } catch {
    return rawURL
  }
}

const loadDownloadSources = () => {
  customDownloadSources.value = []
  try {
    const stored = JSON.parse(localStorage.getItem(sourceStorageKey.value) || '[]')
    if (!Array.isArray(stored)) return
    const builtInURLs = new Set(builtInDownloadSources[coreType.value].map((source) => source.url))
    customDownloadSources.value = stored
      .filter((url): url is string => typeof url === 'string' && url.trim() !== '')
      .map((url) => url.trim())
      .filter((url, index, urls) => !builtInURLs.has(url) && urls.indexOf(url) === index)
      .map((url) => ({ label: sourceLabel(url), url }))
  } catch {
    customDownloadSources.value = []
  }
}

const persistDownloadSources = () => {
  try {
    localStorage.setItem(
      sourceStorageKey.value,
      JSON.stringify(customDownloadSources.value.map((source) => source.url)),
    )
  } catch {
    // Local storage is optional in the desktop webview.
  }
}

const applyConfig = (next: CoreConfig) => {
  const nextCoreType: CoreType = next.coreType === 'mihomo' ? 'mihomo' : 'singbox'
  Object.assign(config, next)
  if (nextCoreType !== props.coreType) emit('update:coreType', nextCoreType)
  urlInput.value = next.urlTemplate
  if (!runArgsDirty.value || nextCoreType !== props.coreType) {
    runArgsInput.value = next.runArgs
    runArgsDirty.value = false
  }
  configURLInput.value = next.configURL
  selectedSourceURL.value = sourceOptions.value.some((source) => source.url === next.urlTemplate)
    ? next.urlTemplate
    : ''
}

const selectDownloadSource = () => {
  if (selectedSourceURL.value) urlInput.value = selectedSourceURL.value
}

const validateAndAddURL = async () => {
  if (isValidatingURL.value || !urlInput.value.trim()) return
  isValidatingURL.value = true
  try {
    const normalizedURL = await CoreService.ValidateURL(urlInput.value.trim())
    if (!sourceOptions.value.some((source) => source.url === normalizedURL)) {
      customDownloadSources.value.push({ label: sourceLabel(normalizedURL), url: normalizedURL })
      persistDownloadSources()
    }
    selectedSourceURL.value = normalizedURL
    applyConfig(await CoreService.SaveURL(normalizedURL))
    showNotification({ content: 'coreURLSaved', type: 'alert-success' })
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  } finally {
    isValidatingURL.value = false
  }
}

const saveRunArgs = async () => {
  if (isSavingRunArgs.value || config.running) return
  isSavingRunArgs.value = true
  try {
    const next = await CoreService.SaveRunArgs(runArgsInput.value, coreType.value)
    runArgsDirty.value = false
    applyConfig(next)
    showNotification({ content: 'coreRunArgsSaved', type: 'alert-success' })
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  } finally {
    isSavingRunArgs.value = false
  }
}

const startCore = async () => {
  if (isStarting.value || config.running) return
  isStarting.value = true
  try {
    const next = await CoreService.StartCore(runArgsInput.value, coreType.value)
    runArgsDirty.value = false
    applyConfig(next)
    showNotification({ content: 'coreStarted', type: 'alert-success' })
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  } finally {
    isStarting.value = false
  }
}

const stopCore = async () => {
  if (isStopping.value || !config.running) return
  isStopping.value = true
  try {
    applyConfig(await CoreService.StopCore())
    showNotification({ content: 'coreStoppedSuccess', type: 'alert-success' })
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  } finally {
    isStopping.value = false
  }
}

const restartCore = async () => {
  if (isRestarting.value || !config.installed) return
  isRestarting.value = true
  try {
    const next = await CoreService.RestartCore(runArgsInput.value, coreType.value)
    runArgsDirty.value = false
    applyConfig(next)
    showNotification({ content: 'coreStarted', type: 'alert-success' })
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  } finally {
    isRestarting.value = false
  }
}

const downloadConfig = async () => {
  if (isDownloadingConfig.value || !configURLInput.value.trim()) return
  isDownloadingConfig.value = true
  try {
    applyConfig(await CoreService.DownloadConfig(configURLInput.value))
    showNotification({ content: 'coreConfigDownloadSuccess', type: 'alert-success' })
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  } finally {
    isDownloadingConfig.value = false
  }
}

const loadConfig = async () => {
  if (isRefreshing.value) return
  isRefreshing.value = true
  try {
    applyConfig(await CoreService.GetConfig())
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  } finally {
    isRefreshing.value = false
  }
}

const saveBehavior = async () => {
  if (isSavingBehavior.value) return
  isSavingBehavior.value = true
  try {
    applyConfig(
      await CoreService.SaveBehavior(
        config.runAsAdmin,
        config.autoStart,
        config.autoStartCore,
        config.backendDebugLog,
      ),
    )
    showNotification({ content: 'coreBehaviorSaved', type: 'alert-success' })
  } catch (error) {
    await loadConfig()
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  } finally {
    isSavingBehavior.value = false
  }
}

const saveURL = async () => {
  if (isSaving.value || !urlInput.value.trim()) return
  isSaving.value = true
  try {
    applyConfig(await CoreService.SaveURL(urlInput.value))
    showNotification({ content: 'coreURLSaved', type: 'alert-success' })
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  } finally {
    isSaving.value = false
  }
}

const checkUpdate = async () => {
  if (isChecking.value || !urlInput.value.trim()) return
  isChecking.value = true
  try {
    applyConfig(await CoreService.CheckUpdate(version.value || ''))
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  } finally {
    isChecking.value = false
  }
}

const downloadCore = async () => {
  if (isDownloading.value) return
  isDownloading.value = true
  try {
    applyConfig(await CoreService.DownloadCore(version.value || ''))
    showNotification({ content: 'coreDownloadSuccess', type: 'alert-success' })
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  } finally {
    isDownloading.value = false
  }
}

onMounted(() => {
  loadDownloadSources()
  void loadConfig()
  refreshTimer = setInterval(() => {
    if (!isStarting.value && !isStopping.value && !isRestarting.value && !isDownloading.value) {
      void loadConfig()
    }
  }, 1000)
})

watch(
  () => props.coreType,
  () => {
    loadDownloadSources()
    selectedSourceURL.value = ''
    void loadConfig()
  },
)

onUnmounted(() => {
  if (refreshTimer) {
    clearInterval(refreshTimer)
    refreshTimer = undefined
  }
})
</script>
