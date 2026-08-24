<template>
  <div class="flex flex-col gap-3 text-sm">
    <template v-if="props.activeTab !== 'settings'">
      <section>
        <div class="text-base-content/85 mt-1 mb-2.5 px-1 text-base font-semibold tracking-tight">
          {{ $t('coreRun') }}
        </div>
        <div class="settings-grid">
          <div class="setting-item">
            <span class="setting-item-label">{{ $t('coreRunStatus') }}</span>
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
              :disabled="
                config.running || isStarting || isStopping || isRestarting || isSavingRunArgs
              "
              @input="touchDraft('runArgs')"
              @change="saveRunArgs"
              @keydown.enter.prevent="saveRunArgs"
            />

            <div class="flex flex-wrap gap-2 self-start">
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
            </div>
          </div>
        </div>
      </section>

      <section>
        <div class="text-base-content/85 mt-1 mb-2.5 flex items-center justify-between px-1">
          <span class="text-base font-semibold tracking-tight">{{ $t('coreMaintenance') }}</span>
          <button
            class="btn btn-circle btn-sm"
            type="button"
            :aria-label="$t('coreAdvanced')"
            :title="$t('coreAdvanced')"
            @click="advancedOpen = true"
          >
            <Cog6ToothIcon class="h-4 w-4" />
          </button>
        </div>
        <div class="settings-grid">
          <div class="setting-item gap-3 max-sm:flex-col max-sm:items-start! max-sm:py-3">
            <div class="flex min-w-0 flex-wrap items-center gap-2">
              <span class="setting-item-label">{{ $t('coreSettings') }}</span>
              <span
                class="badge badge-sm"
                :class="config.installed ? 'badge-success' : 'badge-ghost'"
              >
                {{ installedVersionLabel }}
              </span>
              <span
                v-if="config.latestVersion"
                class="badge badge-sm"
                :class="config.updateAvailable ? 'badge-warning' : 'badge-ghost'"
              >
                {{ config.latestVersion }}
              </span>
              <button
                class="btn btn-circle btn-ghost btn-xs"
                type="button"
                :aria-label="$t('checkUpdate')"
                :title="$t('checkUpdate')"
                :disabled="isChecking || isDownloading || isSaving || !urlInput.trim()"
                @click="checkUpdate()"
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
            <button
              class="btn btn-sm max-sm:self-stretch"
              :class="{ 'btn-primary': !config.installed }"
              :disabled="isCoreMaintenanceBusy"
              @click="maintainCore"
            >
              <span
                v-if="isCoreMaintenanceBusy"
                class="loading loading-spinner h-4 w-4"
              ></span>
              <ArrowDownCircleIcon
                v-else
                class="h-4 w-4"
              />
              {{ config.installed ? $t('updateCore') : $t('installCore') }}
            </button>
          </div>
        </div>
      </section>

      <section>
        <div class="text-base-content/85 mt-1 mb-2.5 px-1 text-base font-semibold tracking-tight">
          {{ $t('coreConfig') }}
        </div>
        <div class="settings-grid">
          <div class="setting-item grid grid-cols-[5rem_minmax(0,1fr)] items-center gap-2 py-3">
            <span class="setting-item-label">{{ $t('coreConfigURL') }}</span>
            <input
              v-model="configURLInput"
              class="input input-sm w-full min-w-0"
              type="url"
              :aria-label="$t('coreConfigURL')"
              :placeholder="$t('coreConfigURLPlaceholder')"
              :disabled="isDownloadingConfig || isImportingConfig"
              @input="touchDraft('configURL')"
            />
          </div>

          <div
            class="setting-item grid grid-cols-[auto_minmax(7rem,10rem)_auto_auto] items-center gap-2 py-3"
          >
            <span class="setting-item-label">{{ $t('coreConfigSaveTo') }}</span>
            <input
              v-model="configFileNameInput"
              class="input input-sm w-full min-w-0 font-mono"
              type="text"
              :aria-label="$t('coreConfigSaveTo')"
              :placeholder="defaultConfigFileName"
              :disabled="isSavingConfigFileName || isDownloadingConfig || isImportingConfig"
              @input="touchDraft('configFileName')"
              @change="saveConfigFileName()"
              @keydown.enter.prevent="saveConfigFileName()"
            />
            <button
              class="btn btn-sm"
              type="button"
              :disabled="isDownloadingConfig || isImportingConfig || !configURLInput.trim()"
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
            <button
              class="btn btn-sm"
              type="button"
              :disabled="isDownloadingConfig || isImportingConfig || !configFileNameInput.trim()"
              @click="openConfigFilePicker"
            >
              <span
                v-if="isImportingConfig"
                class="loading loading-spinner h-4 w-4"
              ></span>
              <ArrowUpTrayIcon
                v-else
                class="h-4 w-4"
              />
              {{ $t('coreImportConfig') }}
            </button>
            <input
              ref="configFileInput"
              class="hidden"
              type="file"
              :accept="configFileAccept"
              @change="importConfig"
            />
          </div>

          <div
            class="setting-item grid grid-cols-[auto_minmax(7rem,10rem)_auto_auto] items-center gap-2 py-3"
          >
            <span class="setting-item-label">{{ $t('coreActiveConfig') }}</span>
            <select
              v-model="activeConfigFile"
              class="select select-sm w-full min-w-0 font-mono"
              :aria-label="$t('coreActiveConfig')"
              :disabled="
                config.running ||
                isStarting ||
                isStopping ||
                isRestarting ||
                isSelectingConfigFile ||
                isDeletingConfigFile ||
                isRevertingConfigFile
              "
              @focus="scanConfigFiles(false)"
              @mousedown="scanConfigFiles(false)"
              @change="handleSelectConfigFile"
            >
              <option
                v-if="availableConfigFiles.length === 0"
                disabled
                value=""
              >
                {{ $t('noConfigFilesFound') }}
              </option>
              <option
                v-for="file in availableConfigFiles"
                :key="file"
                :value="file"
              >
                {{ file }}
              </option>
            </select>
            <button
              class="btn btn-sm text-error"
              type="button"
              :disabled="
                config.running ||
                isStarting ||
                isStopping ||
                isRestarting ||
                isSelectingConfigFile ||
                isDeletingConfigFile ||
                isUndoingDelete ||
                !activeConfigFile ||
                availableConfigFiles.length === 0
              "
              @click="deleteActiveConfigFile"
            >
              <span
                v-if="isDeletingConfigFile"
                class="loading loading-spinner h-4 w-4"
              ></span>
              <TrashIcon
                v-else
                class="h-4 w-4"
              />
              {{ $t('delete') }}
            </button>
            <button
              class="btn btn-sm"
              type="button"
              :disabled="
                config.running ||
                isStarting ||
                isStopping ||
                isRestarting ||
                isSelectingConfigFile ||
                isDeletingConfigFile ||
                isUndoingDelete ||
                !canUndoDelete
              "
              @click="undoDeleteConfigFile"
            >
              <span
                v-if="isUndoingDelete"
                class="loading loading-spinner h-4 w-4"
              ></span>
              <ArrowUturnLeftIcon
                v-else
                class="h-4 w-4"
              />
              {{ $t('undo') }}
            </button>
          </div>
        </div>
      </section>

      <DialogWrapper
        v-model="advancedOpen"
        :title="$t('coreAdvanced')"
      >
        <div class="settings-grid">
          <div class="setting-item">
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

          <label class="setting-item max-sm:flex-col max-sm:items-stretch! max-sm:py-3">
            <span class="setting-item-label">{{ $t('coreDownloadSource') }}</span>
            <select
              v-model="selectedSourceURL"
              class="select select-sm min-w-44 max-sm:w-full"
              :aria-label="$t('coreDownloadSource')"
              :disabled="isSaving || isValidatingURL || isDownloading"
              @change="selectDownloadSource"
            >
              <option
                v-for="source in sourceOptions"
                :key="source.url"
                :value="sourceURL(source)"
              >
                {{ source.label }}
              </option>
            </select>
          </label>

          <label class="setting-item flex-col !items-stretch py-3">
            <span class="setting-item-label">{{ $t('coreDownloadURL') }}</span>
            <input
              v-model="urlInput"
              class="input input-sm w-full"
              type="url"
              :placeholder="$t('coreDownloadURLPlaceholder')"
              :disabled="isSaving || isValidatingURL || isDownloading"
              @input="touchDraft('url')"
              @change="saveURL()"
              @keydown.enter.prevent="validateAndAddURL"
            />
          </label>
        </div>
      </DialogWrapper>
    </template>

    <template v-else>
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
              @change="saveBehavior()"
            />
          </label>
          <label class="setting-item">
            <span class="setting-item-label">{{ $t('coreAutoStart') }}</span>
            <input
              v-model="behaviorDraft.autoStart"
              class="toggle"
              type="checkbox"
              :disabled="isSavingBehavior"
              @change="saveBehavior()"
            />
          </label>
          <label class="setting-item">
            <span class="setting-item-label">{{ $t('autoStartSingBox') }}</span>
            <input
              v-model="behaviorDraft.autoStartSingBox"
              class="toggle"
              type="checkbox"
              :disabled="isSavingBehavior"
              @change="saveBehavior('sing-box')"
            />
          </label>
          <label class="setting-item">
            <span class="setting-item-label">{{ $t('autoStartMihomo') }}</span>
            <input
              v-model="behaviorDraft.autoStartMihomo"
              class="toggle"
              type="checkbox"
              :disabled="isSavingBehavior"
              @change="saveBehavior('mihomo')"
            />
          </label>
          <label class="setting-item">
            <span class="setting-item-label">{{ $t('backendDebugLog') }}</span>
            <input
              v-model="behaviorDraft.backendDebugLog"
              class="toggle"
              type="checkbox"
              :disabled="isSavingBehavior"
              @change="saveBehavior()"
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
              @change="saveBehavior()"
            />
          </label>
        </div>
      </section>

      <section>
        <div class="text-base-content/85 mt-1 mb-2.5 px-1 text-base font-semibold tracking-tight">
          {{ $t('appUpdate') }}
        </div>
        <div class="settings-grid">
          <div class="setting-item gap-3 max-sm:flex-col max-sm:items-start! max-sm:py-3">
            <div class="flex min-w-0 flex-wrap items-center gap-2">
              <span class="setting-item-label">{{ $t('desktopApp') }}</span>
              <span class="badge badge-sm badge-ghost">
                {{ appVersionLabel }}
              </span>
              <span
                v-if="appUpdateInfo.latestVersion"
                class="badge badge-sm"
                :class="appUpdateInfo.updateAvailable ? 'badge-warning' : 'badge-ghost'"
              >
                {{ appUpdateInfo.latestVersion }}
              </span>
              <button
                class="btn btn-circle btn-ghost btn-xs"
                type="button"
                :aria-label="$t('checkUpdate')"
                :title="$t('checkUpdate')"
                :disabled="isCheckingAppUpdate || isUpdatingApp"
                @click="checkAppUpdate()"
              >
                <span
                  v-if="isCheckingAppUpdate"
                  class="loading loading-spinner h-3.5 w-3.5"
                ></span>
                <ArrowPathIcon
                  v-else
                  class="h-3.5 w-3.5"
                />
              </button>
            </div>
            <button
              class="btn btn-sm max-sm:self-stretch"
              :class="{ 'btn-primary': appUpdateInfo.updateAvailable }"
              :disabled="isUpdatingApp || isCheckingAppUpdate || !appUpdateInfo.updateAvailable"
              @click="installAppUpdate()"
            >
              <span
                v-if="isUpdatingApp"
                class="loading loading-spinner h-4 w-4"
              ></span>
              <ArrowDownCircleIcon
                v-else
                class="h-4 w-4"
              />
              {{ $t('updateApp') }}
            </button>
          </div>
        </div>
      </section>
    </template>
  </div>
</template>

<script setup lang="ts">
import * as CoreService from '../../../../bindings/zashdesktop/coreservice'
import type { AppUpdateInfo, CoreConfig } from '../../../../bindings/zashdesktop/models'
import DialogWrapper from '@/components/common/DialogWrapper.vue'
import SegmentedControl, { type SegmentOption } from '@/components/common/SegmentedControl.vue'
import { showNotification } from '@/helper/notification'
import {
  ArrowDownCircleIcon,
  ArrowDownTrayIcon,
  ArrowPathIcon,
  ArrowUpTrayIcon,
  ArrowUturnLeftIcon,
  Cog6ToothIcon,
  PlayIcon,
  StopIcon,
  TrashIcon,
} from '@heroicons/vue/24/outline'
import { useI18n } from 'vue-i18n'
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue'

type CoreType = 'sing-box' | 'mihomo'
type CoreTab = 'sing-box' | 'mihomo' | 'settings'
type CoreChannel = 'stable' | 'test'
type DraftKey = 'url' | 'runArgs' | 'configURL' | 'configFileName' | 'behavior'

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
      channelURLs: {
        stable:
          'https://github.com/llxo/sing-box-releases/releases/download/v{version}/sing-box-{version}-windows-amd64.zip',
        test: 'https://github.com/llxo/sing-box-releases/releases/download/v{version}/sing-box-{version}-windows-amd64.zip',
      },
    },
    {
      label: 'reF1nd/sing-box-releases',
      url: 'https://github.com/reF1nd/sing-box-releases/releases/download/v{version}/sing-box-{version}-windows-amd64.zip',
      channelURLs: {
        stable:
          'https://github.com/reF1nd/sing-box-releases/releases/download/v{version}/sing-box-{version}-windows-amd64.zip',
        test: 'https://github.com/reF1nd/sing-box-releases/releases/download/v{version}/sing-box-{version}-windows-amd64.zip',
      },
    },
    {
      label: 'SagerNet/sing-box',
      url: 'https://github.com/SagerNet/sing-box/releases/download/v{version}/sing-box-{version}-windows-amd64.zip',
      channelURLs: {
        stable:
          'https://github.com/SagerNet/sing-box/releases/download/v{version}/sing-box-{version}-windows-amd64.zip',
        test: 'https://github.com/SagerNet/sing-box/releases/download/v{version}/sing-box-{version}-windows-amd64.zip',
      },
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

const props = withDefaults(
  defineProps<{
    coreType: CoreType
    activeTab?: CoreTab
  }>(),
  {
    activeTab: 'sing-box',
  },
)

const emit = defineEmits<{
  (event: 'update:coreType', value: CoreType): void
}>()

const emptyCoreConfig = (coreType: CoreType): CoreConfig => ({
  coreType,
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
  configFileName: coreType === 'mihomo' ? 'config.yaml' : 'config.json',
  running: false,
  pid: 0,
  logPath: '',
  configPath: '',
  configAvailable: false,
  runAsAdmin: false,
  autoStart: false,
  autoStartSingBox: false,
  autoStartMihomo: false,
  backendDebugLog: false,
  trayAPIURL: 'http://127.0.0.1:9090',
})
const config = reactive<CoreConfig>(emptyCoreConfig(props.coreType))
// Polling replaces config every second. Editable controls must bind to drafts.
const behaviorDraft = reactive({
  runAsAdmin: false,
  autoStart: false,
  autoStartSingBox: false,
  autoStartMihomo: false,
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
const defaultConfigFileName = computed(() =>
  coreType.value === 'mihomo' ? 'config.yaml' : 'config.json',
)
const configFileAccept = computed(() => (coreType.value === 'mihomo' ? '.yaml' : '.json'))
const urlInput = ref('')
const selectedSourceURL = ref('')
const customDownloadSources = ref<DownloadSource[]>([])
const runArgsInput = ref('')
const configURLInput = ref('')
const configFileNameInput = ref('')
const configFileInput = ref<HTMLInputElement | null>(null)
const advancedOpen = ref(false)
// A response may only clean the exact draft revision that it submitted.
const draftState = reactive<Record<DraftKey, { dirty: boolean; revision: number }>>({
  url: { dirty: false, revision: 0 },
  runArgs: { dirty: false, revision: 0 },
  configURL: { dirty: false, revision: 0 },
  configFileName: { dirty: false, revision: 0 },
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
const isSavingConfigFileName = ref(false)
const isImportingConfig = ref(false)
const isSavingBehavior = ref(false)
const isRefreshing = ref(false)
const isCheckingAppUpdate = ref(false)
const isUpdatingApp = ref(false)
const isScanningConfigFiles = ref(false)
const isSelectingConfigFile = ref(false)
const isDeletingConfigFile = ref(false)
const isUndoingDelete = ref(false)
const canUndoDelete = ref(false)
const availableConfigFiles = ref<string[]>([])
const activeConfigFile = ref('')
const appVersion = ref(__APP_VERSION__)
const appUpdateInfo = reactive<AppUpdateInfo>({
  currentVersion: __APP_VERSION__,
  latestVersion: '',
  updateAvailable: false,
  releaseURL: '',
  releaseNotes: '',
  publishedAt: '',
  downloadURL: '',
  assetSize: 0,
})
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
    isSavingConfigFileName.value ||
    isImportingConfig.value ||
    isSavingBehavior.value ||
    isUpdatingApp.value ||
    isSelectingConfigFile.value ||
    isDeletingConfigFile.value ||
    isUndoingDelete.value,
)
const currentChannel = computed<CoreChannel>(() => (config.channel === 'test' ? 'test' : 'stable'))
const installedVersionLabel = computed(() => {
  if (!config.installed) return t('coreNotInstalled')
  return config.installedVersion || config.version || t('coreInstalled')
})
const isCoreMaintenanceBusy = computed(
  () => isSaving.value || isValidatingURL.value || isDownloading.value,
)
let refreshTimer: ReturnType<typeof setInterval> | undefined
let refreshRequest = 0
let checkSequence = 0
type ConfigRequest = { id: number; coreType: CoreType; allowCoreTypeChange: boolean }
const beginConfigRequest = (allowCoreTypeChange = false): ConfigRequest => {
  isRefreshing.value = false
  return { id: ++refreshRequest, coreType: coreType.value, allowCoreTypeChange }
}
const isCurrentConfigRequest = (request: ConfigRequest) =>
  request.id === refreshRequest && request.coreType === coreType.value
const applyCurrentConfig = (request: ConfigRequest, next: CoreConfig, forceDrafts = false) => {
  if (!isCurrentConfigRequest(request)) return false
  const responseCoreType: CoreType = next.coreType === 'mihomo' ? 'mihomo' : 'sing-box'
  if (!request.allowCoreTypeChange && responseCoreType !== request.coreType) return false
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
    if (urlInput.value !== next.urlTemplate) {
      urlInput.value = next.urlTemplate
    }
    resetDraft('url')
    const matchURL = sourceOptions.value.some(
      (source) => sourceURL(source) === next.urlTemplate,
    )
      ? next.urlTemplate
      : ''
    if (selectedSourceURL.value !== matchURL) {
      selectedSourceURL.value = matchURL
    }
  }
  if (syncAllDrafts || !draftState.runArgs.dirty) {
    if (runArgsInput.value !== next.runArgs) {
      runArgsInput.value = next.runArgs
    }
    resetDraft('runArgs')
  }
  if (syncAllDrafts || !draftState.configURL.dirty) {
    if (configURLInput.value !== next.configURL) {
      configURLInput.value = next.configURL
    }
    resetDraft('configURL')
  }
  if (syncAllDrafts || !draftState.configFileName.dirty) {
    const expectedName = next.configFileName || defaultConfigFileName.value
    if (configFileNameInput.value !== expectedName) {
      configFileNameInput.value = expectedName
    }
    resetDraft('configFileName')
  }
  if (syncAllDrafts || !draftState.behavior.dirty) {
    if (
      behaviorDraft.runAsAdmin !== next.runAsAdmin ||
      behaviorDraft.autoStart !== next.autoStart ||
      behaviorDraft.autoStartSingBox !== next.autoStartSingBox ||
      behaviorDraft.autoStartMihomo !== next.autoStartMihomo ||
      behaviorDraft.backendDebugLog !== next.backendDebugLog ||
      behaviorDraft.trayAPIURL !== next.trayAPIURL
    ) {
      Object.assign(behaviorDraft, {
        runAsAdmin: next.runAsAdmin,
        autoStart: next.autoStart,
        autoStartSingBox: next.autoStartSingBox,
        autoStartMihomo: next.autoStartMihomo,
        backendDebugLog: next.backendDebugLog,
        trayAPIURL: next.trayAPIURL,
      })
    }
    resetDraft('behavior')
  }
  syncActiveConfigFile()
}

const extractConfigFileFromRunArgs = (runArgs: string, core: CoreType): string => {
  const trimmed = runArgs.trim()
  if (core === 'mihomo') {
    const match = /(?:^|\s)-f\s+([^\s]+)/i.exec(trimmed)
    return match ? match[1].split(/[\\/]/).pop() || '' : ''
  }
  const match = /(?:^|\s)-c\s+([^\s]+)/i.exec(trimmed)
  return match ? match[1].split(/[\\/]/).pop() || '' : ''
}

const syncActiveConfigFile = () => {
  const fromArgs = extractConfigFileFromRunArgs(config.runArgs, coreType.value)
  if (fromArgs) {
    activeConfigFile.value = fromArgs
    return
  }
  if (config.configFileName) {
    activeConfigFile.value = config.configFileName
    return
  }
  if (availableConfigFiles.value.length > 0) {
    activeConfigFile.value = availableConfigFiles.value[0]
  }
}

const scanConfigFiles = async (notify = false) => {
  if (isScanningConfigFiles.value) return
  isScanningConfigFiles.value = true
  try {
    const files = await CoreService.ListConfigFiles(coreType.value)
    availableConfigFiles.value = files || []
    syncActiveConfigFile()
  } catch (error) {
    if (notify) {
      showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
    }
  } finally {
    isScanningConfigFiles.value = false
  }
}

const handleSelectConfigFile = async () => {
  if (isSelectingConfigFile.value || !activeConfigFile.value || config.running) return
  isSelectingConfigFile.value = true
  const request = beginConfigRequest()
  try {
    const next = await CoreService.SelectConfigFile(activeConfigFile.value, request.coreType)
    if (applyCurrentConfig(request, next)) {
      showNotification({ content: 'coreActiveConfigSaved', type: 'alert-success' })
    }
  } catch (error) {
    if (isCurrentConfigRequest(request)) {
      showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
      syncActiveConfigFile()
    }
  } finally {
    isSelectingConfigFile.value = false
  }
}

const checkCanUndoDelete = async () => {
  try {
    canUndoDelete.value = await CoreService.CanUndoDeleteConfigFile(coreType.value)
  } catch {
    canUndoDelete.value = false
  }
}

const deleteActiveConfigFile = async () => {
  if (
    isDeletingConfigFile.value ||
    !activeConfigFile.value ||
    config.running ||
    availableConfigFiles.value.length === 0
  )
    return
  isDeletingConfigFile.value = true
  const request = beginConfigRequest()
  try {
    const next = await CoreService.DeleteConfigFile(activeConfigFile.value, request.coreType)
    if (applyCurrentConfig(request, next)) {
      canUndoDelete.value = true
      showNotification({ content: 'coreConfigFileDeleted', type: 'alert-success' })
      await scanConfigFiles(false)
    }
  } catch (error) {
    if (isCurrentConfigRequest(request)) {
      showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
    }
  } finally {
    isDeletingConfigFile.value = false
  }
}

const undoDeleteConfigFile = async () => {
  if (isUndoingDelete.value || config.running || !canUndoDelete.value) return
  isUndoingDelete.value = true
  const request = beginConfigRequest()
  try {
    const next = await CoreService.UndoDeleteConfigFile(request.coreType)
    if (applyCurrentConfig(request, next)) {
      canUndoDelete.value = false
      showNotification({ content: 'coreConfigFileRestored', type: 'alert-success' })
      await scanConfigFiles(false)
    }
  } catch (error) {
    if (isCurrentConfigRequest(request)) {
      showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
      await checkCanUndoDelete()
    }
  } finally {
    isUndoingDelete.value = false
  }
}

const selectDownloadSource = async () => {
  if (selectedSourceURL.value) {
    urlInput.value = selectedSourceURL.value
    touchDraft('url')
    await saveURL(false)
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
    if (
      coreType.value === 'mihomo' &&
      channelSource &&
      (selectedBuiltInSource || !next.urlTemplate)
    ) {
      next = await CoreService.SaveURL(sourceURL(channelSource, rawChannel), coreType.value)
    }
    if (applyCurrentConfig(request, next)) {
      await checkUpdate()
    }
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
    await checkUpdate()
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
    syncActiveConfigFile()
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
  if (
    isDownloadingConfig.value ||
    isImportingConfig.value ||
    !configURLInput.value.trim() ||
    (draftState.configFileName.dirty && !(await saveConfigFileName()))
  )
    return
  isDownloadingConfig.value = true
  const request = beginConfigRequest()
  const draftRevision = beginDraftSave('configURL')
  try {
    const next = await CoreService.DownloadConfig(configURLInput.value, coreType.value)
    if (!isCurrentConfigRequest(request)) return
    commitDraftSave('configURL', draftRevision)
    applyCurrentConfig(request, next)
    void scanConfigFiles(false)
    showNotification({ content: 'coreConfigDownloadSuccess', type: 'alert-success' })
  } catch (error) {
    if (isCurrentConfigRequest(request)) {
      showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
    }
  } finally {
    isDownloadingConfig.value = false
  }
}

let configFileNameSavePromise: Promise<boolean> | null = null
const saveConfigFileName = () => {
  if (configFileNameSavePromise) return configFileNameSavePromise
  if (!configFileNameInput.value.trim()) return Promise.resolve(false)
  isSavingConfigFileName.value = true
  const request = beginConfigRequest()
  const draftRevision = beginDraftSave('configFileName')
  configFileNameSavePromise = (async () => {
    try {
      const next = await CoreService.SaveConfigFileName(configFileNameInput.value, request.coreType)
      if (!isCurrentConfigRequest(request)) return false
      commitDraftSave('configFileName', draftRevision)
      applyCurrentConfig(request, next)
      return true
    } catch (error) {
      if (isCurrentConfigRequest(request)) {
        showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
      }
      return false
    } finally {
      isSavingConfigFileName.value = false
      configFileNameSavePromise = null
    }
  })()
  return configFileNameSavePromise
}

const openConfigFilePicker = () => {
  configFileInput.value?.click()
}

const importConfig = async (event: Event) => {
  const input = event.currentTarget as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return
  let request: ConfigRequest | null = null
  try {
    if (draftState.configFileName.dirty && !(await saveConfigFileName())) return
    isImportingConfig.value = true
    request = beginConfigRequest()
    const next = await CoreService.ImportConfig(await file.text(), file.name, request.coreType)
    if (!applyCurrentConfig(request, next)) return
    void scanConfigFiles(false)
    showNotification({ content: 'coreConfigImportSuccess', type: 'alert-success' })
  } catch (error) {
    if (!request || isCurrentConfigRequest(request)) {
      showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
    }
  } finally {
    isImportingConfig.value = false
    input.value = ''
  }
}

const loadConfig = async (useActiveCore = false, forceDrafts = false) => {
  const request = beginConfigRequest(useActiveCore)
  isRefreshing.value = true
  try {
    const next = useActiveCore
      ? await CoreService.GetConfig()
      : await CoreService.GetConfigForType(request.coreType)
    return applyCurrentConfig(request, next, forceDrafts)
  } catch (error) {
    if (forceDrafts && isCurrentConfigRequest(request)) {
      showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
    }
    return false
  } finally {
    if (isCurrentConfigRequest(request)) {
      isRefreshing.value = false
    }
  }
}

const saveBehavior = async (changedCoreType?: CoreType) => {
  if (isSavingBehavior.value) return
  if (changedCoreType === 'sing-box' && behaviorDraft.autoStartSingBox) {
    behaviorDraft.autoStartMihomo = false
  } else if (changedCoreType === 'mihomo' && behaviorDraft.autoStartMihomo) {
    behaviorDraft.autoStartSingBox = false
  }
  isSavingBehavior.value = true
  const request = beginConfigRequest()
  const draftRevision = beginDraftSave('behavior')
  try {
    const next = await CoreService.SaveBehavior(
      behaviorDraft.runAsAdmin,
      behaviorDraft.autoStart,
      behaviorDraft.autoStartSingBox,
      behaviorDraft.autoStartMihomo,
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

const saveURL = async (notify = true, refreshRemote = true) => {
  if (isSaving.value || !urlInput.value.trim()) return false
  isSaving.value = true
  const request = beginConfigRequest()
  const draftRevision = beginDraftSave('url')
  try {
    const next = await CoreService.SaveURL(urlInput.value.trim(), coreType.value)
    if (!isCurrentConfigRequest(request)) return
    rememberDownloadSource(next.urlTemplate)
    commitDraftSave('url', draftRevision)
    applyCurrentConfig(request, next)
    if (notify) {
      showNotification({ content: 'coreURLSaved', type: 'alert-success' })
    }
    if (refreshRemote) {
      await checkUpdate()
    }
    return true
  } catch (error) {
    if (isCurrentConfigRequest(request)) {
      showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
    }
    return false
  } finally {
    isSaving.value = false
  }
}

const checkUpdate = async (notifyError = true) => {
  if (isChecking.value || !urlInput.value.trim()) return null
  const sequence = ++checkSequence
  isChecking.value = true
  const request = beginConfigRequest()
  try {
    const next = await CoreService.CheckUpdate('', request.coreType)
    return applyCurrentConfig(request, next) ? next : null
  } catch (error) {
    if (notifyError && isCurrentConfigRequest(request)) {
      showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
    }
    return null
  } finally {
    if (sequence === checkSequence) {
      isChecking.value = false
    }
  }
}

const downloadCore = async () => {
  if (isDownloading.value) return
  isDownloading.value = true
  const request = beginConfigRequest()
  try {
    const next = await CoreService.DownloadCore('', request.coreType)
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

const maintainCore = async () => {
  if (!urlInput.value.trim()) {
    advancedOpen.value = true
    return
  }
  if (draftState.url.dirty && !(await saveURL(false, false))) return
  await downloadCore()
}

const appVersionLabel = computed(() => {
  const v = appVersion.value || __APP_VERSION__
  return v ? (v.startsWith('v') ? v : `v${v}`) : 'v0.0.0'
})

const checkAppUpdate = async (notify = true) => {
  if (isCheckingAppUpdate.value || isUpdatingApp.value) return
  isCheckingAppUpdate.value = true
  try {
    const info = await CoreService.CheckAppUpdate()
    Object.assign(appUpdateInfo, info)
    if (info.currentVersion) {
      appVersion.value = info.currentVersion
    }
    if (notify) {
      if (info.updateAvailable) {
        showNotification({ content: 'coreUpdateAvailable', type: 'alert-info' })
      } else {
        showNotification({ content: 'appUpToDate', type: 'alert-success' })
      }
    }
  } catch (error) {
    if (notify) {
      showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
    }
  } finally {
    isCheckingAppUpdate.value = false
  }
}

const installAppUpdate = async () => {
  if (isUpdatingApp.value || isCheckingAppUpdate.value) return
  isUpdatingApp.value = true
  try {
    showNotification({ content: 'appUpdating', type: 'alert-info' })
    await CoreService.InstallAppUpdate()
    showNotification({ content: 'appUpdateSuccess', type: 'alert-success' })
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
    isUpdatingApp.value = false
  }
}

onMounted(() => {
  loadDownloadSources()
  void (async () => {
    try {
      const v = await CoreService.GetAppVersion()
      if (v) appVersion.value = v
      const info = await CoreService.GetAppUpdateInfo()
      if (info) Object.assign(appUpdateInfo, info)
    } catch {
      // ignore
    }
  })()
  void (async () => {
    if (await loadConfig(true, true)) {
      await nextTick()
      void scanConfigFiles(false)
      void checkCanUndoDelete()
      await checkUpdate(false)
    }
  })()
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
    checkSequence += 1
    isChecking.value = false
    isRefreshing.value = false
    availableConfigFiles.value = []
    activeConfigFile.value = ''
    canUndoDelete.value = false
    Object.assign(config, emptyCoreConfig(props.coreType))
    loadDownloadSources()
    resetDraft('url')
    resetDraft('runArgs')
    resetDraft('configURL')
    resetDraft('configFileName')
    resetDraft('behavior')
    selectedSourceURL.value = ''
    advancedOpen.value = false
    void (async () => {
      if (await loadConfig(false, true)) {
        void scanConfigFiles(false)
        void checkCanUndoDelete()
        await checkUpdate(false)
      }
    })()
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
