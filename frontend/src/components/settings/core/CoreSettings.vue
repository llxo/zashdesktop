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
            @input="touchDraft('runArgs')"
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
          <div class="flex items-center justify-between gap-3">
            <span class="setting-item-label">{{ $t('coreChannel') }}</span>
            <div
              :class="{
                'pointer-events-none opacity-60':
                  isSavingChannel || isSaving || isChecking || isDownloading,
              }"
            >
              <SegmentedControl
                :model-value="currentChannel"
                :options="channelOptions"
                @update:model-value="saveChannel"
              />
            </div>
          </div>
          <div class="flex w-full flex-wrap gap-2">
            <select
              v-model="selectedSourceURL"
              class="select select-sm min-w-44 flex-1"
              :aria-label="$t('coreDownloadURL')"
              :disabled="isSaving || isValidatingURL || isDownloading"
              @change="selectDownloadSource"
            >
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
            @input="touchDraft('url')"
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
            @input="touchDraft('configURL')"
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
            v-model="behaviorDraft.runAsAdmin"
            class="toggle"
            type="checkbox"
            :disabled="isSavingBehavior"
            @change="saveBehavior"
          />
        </label>
        <label class="setting-item">
          <span class="setting-item-label">{{ $t('coreAutoStart') }}</span>
          <input
            v-model="behaviorDraft.autoStart"
            class="toggle"
            type="checkbox"
            :disabled="isSavingBehavior"
            @change="saveBehavior"
          />
        </label>
        <label class="setting-item">
          <span class="setting-item-label">{{ $t('coreAutoStartCore') }}</span>
          <input
            v-model="behaviorDraft.autoStartCore"
            class="toggle"
            type="checkbox"
            :disabled="isSavingBehavior"
            @change="saveBehavior"
          />
        </label>
        <label class="setting-item">
          <span class="setting-item-label">{{ $t('backendDebugLog') }}</span>
          <input
            v-model="behaviorDraft.backendDebugLog"
            class="toggle"
            type="checkbox"
            :disabled="isSavingBehavior"
            @change="saveBehavior"
          />
        </label>
        <label class="setting-item flex-col !items-stretch py-3">
          <span class="setting-item-label">{{ $t('trayAPIURL') }}</span>
          <input
            v-model="behaviorDraft.trayAPIURL"
            class="input input-sm w-full"
            type="url"
            :placeholder="$t('trayAPIURLPlaceholder')"
            :disabled="isSavingBehavior"
            @input="touchDraft('behavior')"
            @change="saveBehavior"
          />
        </label>
      </div>
    </section>
  </div>
</template>

<script setup lang="ts">
import * as CoreService from '../../../../bindings/zashdesktop/coreservice'
import type { CoreConfig } from '../../../../bindings/zashdesktop/models'
import { version } from '@/assembly/version'
import SegmentedControl, { type SegmentOption } from '@/components/common/SegmentedControl.vue'
import { showNotification } from '@/helper/notification'
import {
  ArrowDownCircleIcon,
  ArrowDownTrayIcon,
  ArrowPathIcon,
  BookmarkSquareIcon,
  PlayIcon,
  StopIcon,
} from '@heroicons/vue/24/outline'
import { useI18n } from 'vue-i18n'
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'

type CoreType = 'sing-box' | 'mihomo'
type CoreChannel = 'stable' | 'test'
type DraftKey = 'url' | 'runArgs' | 'configURL' | 'behavior'

type DownloadSource = {
  label: string
  url: string
  channelURLs?: Partial<Record<CoreChannel, string>>
}

const builtInDownloadSources: Record<CoreType, DownloadSource[]> = {
  'sing-box': [
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
  mihomo: [
    {
      label: 'MetaCubeX/mihomo',
      url: 'https://github.com/MetaCubeX/mihomo/releases/download/v{version}/mihomo-windows-amd64-v{version}.zip',
      channelURLs: {
        test: 'https://github.com/MetaCubeX/mihomo/releases/download/Prerelease-Alpha/mihomo-windows-amd64-{version}.zip',
      },
    },
  ],
}

const props = defineProps<{
  coreType: CoreType
}>()

const emit = defineEmits<{
  (event: 'update:coreType', value: CoreType): void
}>()

const config = reactive<CoreConfig>({
  coreType: 'sing-box',
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
  trayAPIURL: 'http://127.0.0.1:9090',
})
// Polling replaces config every second. Editable controls must bind to drafts.
const behaviorDraft = reactive({
  runAsAdmin: false,
  autoStart: false,
  autoStartCore: false,
  backendDebugLog: false,
  trayAPIURL: 'http://127.0.0.1:9090',
})
const coreType = computed(() => props.coreType)
const { t } = useI18n()
const channelOptions = computed<SegmentOption[]>(() => [
  { value: 'stable', label: t('coreStableBuild') },
  { value: 'test', label: t('coreTestBuild') },
])
const defaultRunArgsPlaceholder = computed(() =>
  coreType.value === 'mihomo' ? '-d . -f config.yaml' : 'run -c config.json -D .',
)
const urlInput = ref('')
const selectedSourceURL = ref('')
const customDownloadSources = ref<DownloadSource[]>([])
const runArgsInput = ref('')
const configURLInput = ref('')
// A response may only clean the exact draft revision that it submitted.
const draftState = reactive<Record<DraftKey, { dirty: boolean; revision: number }>>({
  url: { dirty: false, revision: 0 },
  runArgs: { dirty: false, revision: 0 },
  configURL: { dirty: false, revision: 0 },
  behavior: { dirty: false, revision: 0 },
})
const touchDraft = (key: DraftKey) => {
  draftState[key].dirty = true
  draftState[key].revision += 1
}
const beginDraftSave = (key: DraftKey) => {
  draftState[key].dirty = true
  return draftState[key].revision
}
const commitDraftSave = (key: DraftKey, revision: number) => {
  if (draftState[key].revision !== revision) return false
  draftState[key].dirty = false
  return true
}
const resetDraft = (key: DraftKey) => {
  draftState[key].dirty = false
}
const isSaving = ref(false)
const isSavingChannel = ref(false)
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
const isConfigMutationPending = computed(
  () =>
    isSaving.value ||
    isSavingChannel.value ||
    isChecking.value ||
    isDownloading.value ||
    isValidatingURL.value ||
    isStarting.value ||
    isStopping.value ||
    isRestarting.value ||
    isSavingRunArgs.value ||
    isDownloadingConfig.value ||
    isSavingBehavior.value,
)
const currentChannel = computed<CoreChannel>(() => (config.channel === 'test' ? 'test' : 'stable'))
let refreshTimer: ReturnType<typeof setInterval> | undefined
let refreshRequest = 0
type ConfigRequest = { id: number; coreType: CoreType }
const beginConfigRequest = (): ConfigRequest => {
  isRefreshing.value = false
  return { id: ++refreshRequest, coreType: coreType.value }
}
const isCurrentConfigRequest = (request: ConfigRequest) =>
  request.id === refreshRequest && request.coreType === coreType.value
const applyCurrentConfig = (request: ConfigRequest, next: CoreConfig, forceDrafts = false) => {
  if (!isCurrentConfigRequest(request)) return false
  applyConfig(next, forceDrafts)
  return true
}
const sourceStorageKey = computed(() => `core-download-sources:${coreType.value}`)
const sourceOptions = computed(() => [
  ...builtInDownloadSources[coreType.value],
  ...customDownloadSources.value,
])
const sourceURL = (source: DownloadSource, channel = currentChannel.value) =>
  source.channelURLs?.[channel] ?? source.url

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
    const builtInURLs = new Set(
      builtInDownloadSources[coreType.value].flatMap((source) => [
        source.url,
        ...Object.values(source.channelURLs ?? {}),
      ]),
    )
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

const rememberDownloadSource = (rawURL: string) => {
  const normalizedURL = rawURL.trim()
  if (!normalizedURL || sourceOptions.value.some((source) => source.url === normalizedURL)) return
  customDownloadSources.value.push({ label: sourceLabel(normalizedURL), url: normalizedURL })
  persistDownloadSources()
}

const applyConfig = (next: CoreConfig, forceDrafts = false) => {
  const nextCoreType: CoreType = next.coreType === 'mihomo' ? 'mihomo' : 'sing-box'
  const coreChanged = nextCoreType !== props.coreType
  const syncAllDrafts = forceDrafts || coreChanged
  Object.assign(config, next)
  if (coreChanged) emit('update:coreType', nextCoreType)
  if (syncAllDrafts || !draftState.url.dirty) {
    urlInput.value = next.urlTemplate
    resetDraft('url')
    selectedSourceURL.value = sourceOptions.value.some(
      (source) => sourceURL(source) === next.urlTemplate,
    )
      ? next.urlTemplate
      : ''
  }
  if (syncAllDrafts || !draftState.runArgs.dirty) {
    runArgsInput.value = next.runArgs
    resetDraft('runArgs')
  }
  if (syncAllDrafts || !draftState.configURL.dirty) {
    configURLInput.value = next.configURL
    resetDraft('configURL')
  }
  if (syncAllDrafts || !draftState.behavior.dirty) {
    Object.assign(behaviorDraft, {
      runAsAdmin: next.runAsAdmin,
      autoStart: next.autoStart,
      autoStartCore: next.autoStartCore,
      backendDebugLog: next.backendDebugLog,
      trayAPIURL: next.trayAPIURL,
    })
    resetDraft('behavior')
  }
}

const selectDownloadSource = () => {
  if (selectedSourceURL.value) {
    urlInput.value = selectedSourceURL.value
    touchDraft('url')
  }
}

const saveChannel = async (rawChannel: string) => {
  if (isSavingChannel.value || rawChannel === currentChannel.value) return
  isSavingChannel.value = true
  const request = beginConfigRequest()
  try {
    let next = await CoreService.SaveChannel(rawChannel, coreType.value)
    if (!isCurrentConfigRequest(request)) return
    const selectedBuiltInSource = builtInDownloadSources[coreType.value].some(
      (source) =>
        source.url === next.urlTemplate ||
        Object.values(source.channelURLs ?? {}).includes(next.urlTemplate),
    )
    const channelSource = builtInDownloadSources[coreType.value].find(
      (source) => sourceURL(source, rawChannel) !== '',
    )
    if (coreType.value === 'mihomo' && channelSource && (selectedBuiltInSource || !next.urlTemplate)) {
      next = await CoreService.SaveURL(sourceURL(channelSource, rawChannel), coreType.value)
    }
    applyCurrentConfig(request, next)
  } catch (error) {
    if (isCurrentConfigRequest(request)) {
      showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
    }
  } finally {
    isSavingChannel.value = false
  }
}

const validateAndAddURL = async () => {
  if (isValidatingURL.value || !urlInput.value.trim()) return
  isValidatingURL.value = true
  const request = beginConfigRequest()
  const draftRevision = beginDraftSave('url')
  try {
    const normalizedURL = await CoreService.ValidateURL(urlInput.value.trim())
    if (!isCurrentConfigRequest(request)) return
    const next = await CoreService.SaveURL(normalizedURL, coreType.value)
    if (!isCurrentConfigRequest(request)) return
    rememberDownloadSource(normalizedURL)
    selectedSourceURL.value = normalizedURL
    commitDraftSave('url', draftRevision)
    applyCurrentConfig(request, next)
    showNotification({ content: 'coreURLSaved', type: 'alert-success' })
  } catch (error) {
    if (isCurrentConfigRequest(request)) {
      showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
    }
  } finally {
    isValidatingURL.value = false
  }
}

const saveRunArgs = async () => {
  if (isSavingRunArgs.value || config.running) return
  isSavingRunArgs.value = true
  const request = beginConfigRequest()
  const draftRevision = beginDraftSave('runArgs')
  try {
    const next = await CoreService.SaveRunArgs(runArgsInput.value, coreType.value)
    if (!isCurrentConfigRequest(request)) return
    commitDraftSave('runArgs', draftRevision)
    applyCurrentConfig(request, next)
    showNotification({ content: 'coreRunArgsSaved', type: 'alert-success' })
  } catch (error) {
    if (isCurrentConfigRequest(request)) {
      showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
    }
  } finally {
    isSavingRunArgs.value = false
  }
}

const startCore = async () => {
  if (isStarting.value || config.running) return
  isStarting.value = true
  const request = beginConfigRequest()
  const draftRevision = beginDraftSave('runArgs')
  try {
    const next = await CoreService.StartCore(runArgsInput.value, coreType.value)
    if (!isCurrentConfigRequest(request)) return
    commitDraftSave('runArgs', draftRevision)
    applyCurrentConfig(request, next)
    showNotification({ content: 'coreStarted', type: 'alert-success' })
  } catch (error) {
    if (isCurrentConfigRequest(request)) {
      showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
    }
  } finally {
    isStarting.value = false
  }
}

const stopCore = async () => {
  if (isStopping.value || !config.running) return
  isStopping.value = true
  const request = beginConfigRequest()
  try {
    const next = await CoreService.StopCore()
    if (applyCurrentConfig(request, next)) {
      showNotification({ content: 'coreStoppedSuccess', type: 'alert-success' })
    }
  } catch (error) {
    if (isCurrentConfigRequest(request)) {
      showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
    }
  } finally {
    isStopping.value = false
  }
}

const restartCore = async () => {
  if (isRestarting.value || !config.installed) return
  isRestarting.value = true
  const request = beginConfigRequest()
  const draftRevision = beginDraftSave('runArgs')
  try {
    const next = await CoreService.RestartCore(runArgsInput.value, coreType.value)
    if (!isCurrentConfigRequest(request)) return
    commitDraftSave('runArgs', draftRevision)
    applyCurrentConfig(request, next)
    showNotification({ content: 'coreStarted', type: 'alert-success' })
  } catch (error) {
    if (isCurrentConfigRequest(request)) {
      showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
    }
  } finally {
    isRestarting.value = false
  }
}

const downloadConfig = async () => {
  if (isDownloadingConfig.value || !configURLInput.value.trim()) return
  isDownloadingConfig.value = true
  const request = beginConfigRequest()
  const draftRevision = beginDraftSave('configURL')
  try {
    const next = await CoreService.DownloadConfig(configURLInput.value, coreType.value)
    if (!isCurrentConfigRequest(request)) return
    commitDraftSave('configURL', draftRevision)
    applyCurrentConfig(request, next)
    showNotification({ content: 'coreConfigDownloadSuccess', type: 'alert-success' })
  } catch (error) {
    if (isCurrentConfigRequest(request)) {
      showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
    }
  } finally {
    isDownloadingConfig.value = false
  }
}

const loadConfig = async (useActiveCore = false, forceDrafts = false) => {
  const request = beginConfigRequest()
  isRefreshing.value = true
  try {
    const next = useActiveCore
      ? await CoreService.GetConfig()
      : await CoreService.GetConfigForType(request.coreType)
    applyCurrentConfig(request, next, forceDrafts)
  } catch (error) {
    if (isCurrentConfigRequest(request)) {
      showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
    }
  } finally {
    if (isCurrentConfigRequest(request)) {
      isRefreshing.value = false
    }
  }
}

const saveBehavior = async () => {
  if (isSavingBehavior.value) return
  isSavingBehavior.value = true
  const request = beginConfigRequest()
  const draftRevision = beginDraftSave('behavior')
  try {
    const next = await CoreService.SaveBehavior(
      behaviorDraft.runAsAdmin,
      behaviorDraft.autoStart,
      behaviorDraft.autoStartCore,
      behaviorDraft.backendDebugLog,
      behaviorDraft.trayAPIURL,
      coreType.value,
    )
    if (!isCurrentConfigRequest(request)) return
    commitDraftSave('behavior', draftRevision)
    applyCurrentConfig(request, next)
    showNotification({ content: 'coreBehaviorSaved', type: 'alert-success' })
  } catch (error) {
    if (isCurrentConfigRequest(request)) {
      showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
    }
  } finally {
    isSavingBehavior.value = false
  }
}

const saveURL = async () => {
  if (isSaving.value || !urlInput.value.trim()) return
  isSaving.value = true
  const request = beginConfigRequest()
  const draftRevision = beginDraftSave('url')
  try {
    const next = await CoreService.SaveURL(urlInput.value.trim(), coreType.value)
    if (!isCurrentConfigRequest(request)) return
    rememberDownloadSource(next.urlTemplate)
    commitDraftSave('url', draftRevision)
    applyCurrentConfig(request, next)
    showNotification({ content: 'coreURLSaved', type: 'alert-success' })
  } catch (error) {
    if (isCurrentConfigRequest(request)) {
      showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
    }
  } finally {
    isSaving.value = false
  }
}

const checkUpdate = async () => {
  if (isChecking.value || !urlInput.value.trim()) return
  isChecking.value = true
  const request = beginConfigRequest()
  try {
    applyCurrentConfig(
      request,
      await CoreService.CheckUpdate(version.value || '', coreType.value),
    )
  } catch (error) {
    if (isCurrentConfigRequest(request)) {
      showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
    }
  } finally {
    isChecking.value = false
  }
}

const downloadCore = async () => {
  if (isDownloading.value) return
  isDownloading.value = true
  const request = beginConfigRequest()
  try {
    const next = await CoreService.DownloadCore(version.value || '', coreType.value)
    if (applyCurrentConfig(request, next)) {
      showNotification({ content: 'coreDownloadSuccess', type: 'alert-success' })
    }
  } catch (error) {
    if (isCurrentConfigRequest(request)) {
      showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
    }
  } finally {
    isDownloading.value = false
  }
}

onMounted(() => {
  loadDownloadSources()
  void loadConfig(true, true)
  refreshTimer = setInterval(() => {
    if (!isRefreshing.value && !isConfigMutationPending.value) {
      void loadConfig()
    }
  }, 1000)
})

watch(
  () => props.coreType,
  () => {
    refreshRequest += 1
    isRefreshing.value = false
    loadDownloadSources()
    resetDraft('url')
    resetDraft('runArgs')
    resetDraft('configURL')
    resetDraft('behavior')
    selectedSourceURL.value = ''
    void loadConfig(false, true)
  },
)

onUnmounted(() => {
  refreshRequest += 1
  isRefreshing.value = false
  if (refreshTimer) {
    clearInterval(refreshTimer)
    refreshTimer = undefined
  }
})
</script>
