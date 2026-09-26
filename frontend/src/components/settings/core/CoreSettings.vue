<template>
  <div class="flex flex-col gap-3 text-sm">
    <template v-if="props.activeTab !== 'settings'">
      <!-- 运行核心 -->
      <section>
        <div class="text-base-content/85 mt-1 mb-2.5 px-1 text-base font-semibold tracking-tight">
          {{ $t('coreRun') }}
        </div>
        <div class="settings-grid">
          <!-- 运行状态 -->
          <div class="setting-item">
            <span class="w-20 sm:w-24 shrink-0 text-sm font-medium whitespace-nowrap">
              {{ $t('coreRunStatus') }}
            </span>
            <div class="flex flex-1 items-center justify-end">
              <span
                class="badge badge-sm"
                :class="config.running ? 'badge-success' : 'badge-ghost'"
              >
                {{ config.running ? $t('coreRunning') : $t('coreStopped') }}
              </span>
            </div>
          </div>

          <!-- 命令行参数 -->
          <div class="setting-item">
            <span class="w-20 sm:w-24 shrink-0 text-sm font-medium whitespace-nowrap">
              {{ $t('coreRunArgs') }}
            </span>
            <input
              v-model="runArgsInput"
              class="input input-sm flex-1 min-w-0 font-mono text-xs"
              type="text"
              :aria-label="$t('coreRunArgs')"
              :placeholder="defaultRunArgsPlaceholder"
              :disabled="
                config.running || isStarting || isStopping || isRestarting || isSavingRunArgs
              "
              @input="isRunArgsDirty = true"
              @change="saveRunArgs"
              @keydown.enter.prevent="saveRunArgs"
            />
          </div>

          <!-- 运行操作 -->
          <div class="setting-item">
            <span class="w-20 sm:w-24 shrink-0 text-sm font-medium whitespace-nowrap">
              {{ $t('actions') }}
            </span>
            <div class="flex flex-1 items-center justify-end gap-2">
              <button
                v-if="!config.running"
                class="btn btn-primary btn-sm min-w-16"
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
                class="btn btn-error btn-sm min-w-16"
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
                class="btn btn-sm min-w-16"
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
                v-if="showCoreLogError"
                class="btn btn-sm text-error min-w-16"
                :disabled="isOpeningLog"
                @click="openCoreLog"
              >
                <span
                  v-if="isOpeningLog"
                  class="loading loading-spinner h-4 w-4"
                ></span>
                <DocumentTextIcon
                  v-else
                  class="h-4 w-4"
                />
                {{ $t('coreOpenLog') }}
              </button>
            </div>
          </div>
        </div>
      </section>

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
                @click="checkUpdate(true, true)"
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
                'pointer-events-none opacity-60':
                  isSavingChannel || isDownloading,
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

      <!-- 配置文件 -->
      <section>
        <div class="text-base-content/85 mt-1 mb-2.5 px-1 text-base font-semibold tracking-tight">
          {{ $t('coreConfig') }}
        </div>
        <div class="settings-grid">
          <!-- 订阅链接输入 -->
          <div class="setting-item">
            <span class="w-20 sm:w-24 shrink-0 text-sm font-medium whitespace-nowrap">
              {{ $t('coreConfigURL') }}
            </span>
            <input
              v-model="configURLInput"
              class="input input-sm flex-1 min-w-0"
              type="url"
              :aria-label="$t('coreConfigURL')"
              :placeholder="$t('coreConfigURLPlaceholder')"
              :disabled="isDownloadingConfig || isImportingConfig"
              @input="isConfigURLDirty = true"
            />
          </div>

          <!-- 保存并下载/导入 -->
          <div class="setting-item">
            <span class="w-20 sm:w-24 shrink-0 text-sm font-medium whitespace-nowrap">
              {{ $t('coreConfigSaveTo') }}
            </span>
            <div class="join flex-1 min-w-0">
              <input
                v-model="saveTargetFileName"
                class="join-item input input-sm flex-1 min-w-0 font-mono"
                type="text"
                :aria-label="$t('coreConfigSaveTo')"
                :placeholder="defaultConfigFileName"
                :disabled="isDownloadingConfig || isImportingConfig"
              />
              <button
                class="join-item btn btn-sm shrink-0 whitespace-nowrap"
                type="button"
                :disabled="isDownloadingConfig || isImportingConfig || !configURLInput.trim() || !saveTargetFileName.trim()"
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
                class="join-item btn btn-sm shrink-0 whitespace-nowrap"
                type="button"
                :disabled="isDownloadingConfig || isImportingConfig || !saveTargetFileName.trim()"
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
            </div>
            <input
              ref="configFileInput"
              class="hidden"
              type="file"
              :accept="configFileAccept"
              @change="importConfig"
            />
          </div>

          <!-- 生效配置切换与删除 -->
          <div class="setting-item">
            <span class="w-20 sm:w-24 shrink-0 text-sm font-medium whitespace-nowrap">
              {{ $t('coreActiveConfig') }}
            </span>
            <div class="join flex-1 min-w-0">
              <select
                v-model="activeConfigFile"
                class="join-item select select-sm flex-1 min-w-0 font-mono"
                :aria-label="$t('coreActiveConfig')"
                :disabled="
                  config.running ||
                  isStarting ||
                  isStopping ||
                  isRestarting ||
                  isSelectingConfigFile ||
                  isDeletingConfigFile ||
                  isUndoingDelete
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
                class="join-item btn btn-sm text-error shrink-0 whitespace-nowrap"
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
                v-if="canUndoDelete"
                class="join-item btn btn-sm shrink-0 whitespace-nowrap"
                type="button"
                :disabled="
                  config.running ||
                  isStarting ||
                  isStopping ||
                  isRestarting ||
                  isSelectingConfigFile ||
                  isDeletingConfigFile ||
                  isUndoingDelete
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
        </div>
      </section>
    </template>

    <template v-else>
      <section>
        <div class="text-base-content/85 mt-1 mb-2.5 px-1 text-base font-semibold tracking-tight">
          {{ $t('coreBehavior') }}
        </div>
        <div class="settings-grid">
          <label class="setting-item">
            <span class="w-20 sm:w-24 shrink-0 text-sm font-medium whitespace-nowrap">
              {{ $t('coreRunAsAdmin') }}
            </span>
            <div class="flex flex-1 justify-end">
              <input
                v-model="config.runAsAdmin"
                class="toggle"
                type="checkbox"
                :disabled="isSavingBehavior"
                @change="saveBehavior()"
              />
            </div>
          </label>
          <label class="setting-item">
            <span class="w-20 sm:w-24 shrink-0 text-sm font-medium whitespace-nowrap">
              {{ $t('coreAutoStart') }}
            </span>
            <div class="flex flex-1 justify-end">
              <input
                v-model="config.autoStart"
                class="toggle"
                type="checkbox"
                :disabled="isSavingBehavior || !config.isAdmin"
                @change="saveBehavior()"
              />
            </div>
          </label>
          <label class="setting-item">
            <span class="w-20 sm:w-24 shrink-0 text-sm font-medium whitespace-nowrap">
              {{ $t('autoStartSingBox') }}
            </span>
            <div class="flex flex-1 justify-end">
              <input
                v-model="config.autoStartSingBox"
                class="toggle"
                type="checkbox"
                :disabled="isSavingBehavior"
                @change="saveBehavior('sing-box')"
              />
            </div>
          </label>
          <label class="setting-item">
            <span class="w-20 sm:w-24 shrink-0 text-sm font-medium whitespace-nowrap">
              {{ $t('autoStartMihomo') }}
            </span>
            <div class="flex flex-1 justify-end">
              <input
                v-model="config.autoStartMihomo"
                class="toggle"
                type="checkbox"
                :disabled="isSavingBehavior"
                @change="saveBehavior('mihomo')"
              />
            </div>
          </label>
          <label class="setting-item">
            <span class="w-20 sm:w-24 shrink-0 text-sm font-medium whitespace-nowrap">
              {{ $t('stopCoreOnExit') }}
            </span>
            <div class="flex flex-1 justify-end">
              <input
                v-model="config.stopCoreOnExit"
                class="toggle"
                type="checkbox"
                :disabled="isSavingBehavior"
                @change="saveBehavior()"
              />
            </div>
          </label>
          <label class="setting-item">
            <span class="w-20 sm:w-24 shrink-0 text-sm font-medium whitespace-nowrap">
              {{ $t('backendDebugLog') }}
            </span>
            <div class="flex flex-1 justify-end">
              <input
                v-model="config.backendDebugLog"
                class="toggle"
                type="checkbox"
                :disabled="isSavingBehavior"
                @change="saveBehavior()"
              />
            </div>
          </label>
        </div>
      </section>

      <section>
        <div class="text-base-content/85 mt-1 mb-2.5 px-1 text-base font-semibold tracking-tight">
          {{ $t('appUpdate') }}
        </div>
        <div class="settings-grid">
          <div class="setting-item">
            <span class="w-20 sm:w-24 shrink-0 text-sm font-medium whitespace-nowrap">
              {{ $t('desktopApp') }}
            </span>
            <div class="flex flex-1 items-center justify-end gap-2">
              <span class="badge badge-sm badge-ghost font-mono whitespace-nowrap">
                {{ appVersionLabel }}
              </span>
              <template v-if="appUpdateInfo.latestVersion && appUpdateInfo.updateAvailable">
                <ArrowRightIcon class="text-base-content/40 h-3.5 w-3.5 shrink-0" />
                <span class="badge badge-sm badge-warning font-mono whitespace-nowrap">
                  {{ appUpdateInfo.latestVersion }}
                </span>
              </template>
              <button
                class="btn btn-circle btn-ghost btn-xs shrink-0"
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
          </div>
          <div class="setting-item">
            <span class="w-20 sm:w-24 shrink-0 text-sm font-medium whitespace-nowrap">
              {{ $t('actions') }}
            </span>
            <div class="flex flex-1 justify-end">
              <button
                class="btn btn-sm min-w-16"
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
        </div>
      </section>
    </template>
  </div>
</template>

<script setup lang="ts">
import * as CoreService from '../../../../bindings/zashdesktop/coreservice'
import type { AppUpdateInfo, CoreConfig } from '../../../../bindings/zashdesktop/models'
import SegmentedControl, { type SegmentOption } from '@/components/common/SegmentedControl.vue'
import { showNotification } from '@/helper/notification'
import { syncManagedBackendFromCore } from '@/store/setup'
import { startBackendSession, stopBackendSession } from '@/assembly/session'
import {
  ArrowDownCircleIcon,
  ArrowDownTrayIcon,
  ArrowPathIcon,
  ArrowRightIcon,
  ArrowUpTrayIcon,
  ArrowUturnLeftIcon,
  DocumentTextIcon,
  PlayIcon,
  StopIcon,
  TrashIcon,
} from '@heroicons/vue/24/outline'
import { useI18n } from 'vue-i18n'
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { Events } from '@wailsio/runtime'

type CoreType = 'sing-box' | 'mihomo'
type CoreTab = 'sing-box' | 'mihomo' | 'settings'
type CoreChannel = 'stable' | 'test'

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
      label: 'MetaCubeX/mihomo (官方)',
      url: 'https://github.com/MetaCubeX/mihomo/releases/download/v{version}/mihomo-windows-amd64-compatible-v{version}.zip',
      channelURLs: {
        stable:
          'https://github.com/MetaCubeX/mihomo/releases/download/v{version}/mihomo-windows-amd64-compatible-v{version}.zip',
        test: 'https://github.com/MetaCubeX/mihomo/releases/download/Prerelease-Alpha/mihomo-windows-amd64-compatible-{version}.zip',
      },
    },
    {
      label: 'vernesong/mihomo (Smart)',
      url: 'https://github.com/vernesong/mihomo/releases/download/Prerelease-Alpha/mihomo-windows-amd64-v2-go120-{version}.zip',
      channelURLs: {
        stable:
          'https://github.com/vernesong/mihomo/releases/download/Prerelease-Alpha/mihomo-windows-amd64-v2-go120-{version}.zip',
        test: 'https://github.com/vernesong/mihomo/releases/download/Prerelease-Alpha/mihomo-windows-amd64-v2-go120-{version}.zip',
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
  coreLogError: false,
  configPath: '',
  configAvailable: false,
  runAsAdmin: false,
  isAdmin: false,
  autoStart: false,
  autoStartSingBox: false,
  autoStartMihomo: false,
  backendDebugLog: false,
  stopCoreOnExit: true,
  clashApiUrl: '',
  clashApiHost: '127.0.0.1',
  clashApiPort: '9090',
  clashApiSecret: '',
})

const config = reactive<CoreConfig>(emptyCoreConfig(props.coreType))
const coreType = computed(() => props.coreType)
const { t } = useI18n()

const channelOptions = computed<SegmentOption[]>(() => [
  { value: 'stable', label: t('coreStableBuild') },
  { value: 'test', label: t('coreTestBuild') },
])
const defaultRunArgsPlaceholder = computed(() =>
  coreType.value === 'mihomo' ? '-d . -f "config.yaml"' : 'run -c "config.json" -D .',
)
const defaultConfigFileName = computed(() =>
  coreType.value === 'mihomo' ? 'config.yaml' : 'config.json',
)
const configFileAccept = computed(() => (coreType.value === 'mihomo' ? '.yaml,.yml' : '.json'))
const saveTargetFileNameMap = reactive<Record<CoreType, string>>({
  'sing-box': 'config.json',
  'mihomo': 'config.yaml',
})
const saveTargetFileName = computed({
  get: () => saveTargetFileNameMap[coreType.value] || defaultConfigFileName.value,
  set: (val: string) => {
    saveTargetFileNameMap[coreType.value] = val
  },
})

const runArgsInput = ref('')
const configURLInput = ref('')
const isRunArgsDirty = ref(false)
const isConfigURLDirty = ref(false)
const configFileInput = ref<HTMLInputElement | null>(null)

const isSavingChannel = ref(false)
const isChecking = ref(false)
const isDownloading = ref(false)
const isStarting = ref(false)
const isStopping = ref(false)
const isRestarting = ref(false)
const isOpeningLog = ref(false)
const showCoreLogError = computed(() => !config.running && Boolean(config.coreLogError))
const isSavingRunArgs = ref(false)
const isDownloadingConfig = ref(false)
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

const currentChannel = computed<CoreChannel>(() => (config.channel === 'test' ? 'test' : 'stable'))
const installedVersionLabel = computed(() => {
  if (!config.installed) return t('coreNotInstalled')
  return config.installedVersion || config.version || t('coreInstalled')
})


let activeRequestId = 0
let checkSequence = 0

const applyConfig = (next: CoreConfig, forceInputs = false) => {
  const nextCoreType: CoreType = next.coreType === 'mihomo' ? 'mihomo' : 'sing-box'
  const coreChanged = nextCoreType !== props.coreType
  Object.assign(config, next)
  if (coreChanged) emit('update:coreType', nextCoreType)

  if (forceInputs || coreChanged || !isRunArgsDirty.value) {
    runArgsInput.value = next.runArgs || ''
    isRunArgsDirty.value = false
  }
  if (forceInputs || coreChanged || !isConfigURLDirty.value) {
    configURLInput.value = next.configURL || ''
    isConfigURLDirty.value = false
  }
  syncActiveConfigFile()
}

const runAction = async (
  action: () => Promise<CoreConfig>,
  targetCore = coreType.value,
  forceInputs = false,
): Promise<CoreConfig | null> => {
  const reqId = ++activeRequestId
  try {
    const next = await action()
    if (reqId === activeRequestId && targetCore === coreType.value) {
      applyConfig(next, forceInputs)
      return next
    }
    return null
  } catch (error) {
    if (reqId === activeRequestId && targetCore === coreType.value) {
      const errStr = String(error)
      if (errStr.includes('core is already running')) {
        showNotification({ content: 'coreAlreadyRunning', type: 'alert-error' })
      } else {
        showNotification({ content: errStr, type: 'alert-error', timeout: 0 })
      }
    }
    throw error
  }
}

const sourceOptions = computed(() => builtInDownloadSources[coreType.value])
const sourceURL = (source: DownloadSource, channel = currentChannel.value) =>
  source.channelURLs?.[channel] ?? source.url

const sourceStorageKey = computed(() => `core-download-source:${coreType.value}`)
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
  void checkUpdate(false)
}

const syncActiveConfigFile = () => {
  const target = config.configFileName || defaultConfigFileName.value
  if (availableConfigFiles.value.length === 0) {
    activeConfigFile.value = target
    return
  }
  if (availableConfigFiles.value.includes(target)) {
    activeConfigFile.value = target
    return
  }
  if (availableConfigFiles.value.includes(defaultConfigFileName.value)) {
    activeConfigFile.value = defaultConfigFileName.value
    return
  }
  activeConfigFile.value = availableConfigFiles.value[0]
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
  try {
    await runAction(() =>
      CoreService.SelectConfigFile(activeConfigFile.value, coreType.value),
    )
  } catch {
    syncActiveConfigFile()
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
  try {
    const next = await runAction(() =>
      CoreService.DeleteConfigFile(activeConfigFile.value, coreType.value),
    )
    if (next) {
      canUndoDelete.value = true
      showNotification({ content: 'coreConfigFileDeleted', type: 'alert-success' })
      await scanConfigFiles(false)
    }
  } finally {
    isDeletingConfigFile.value = false
  }
}

const undoDeleteConfigFile = async () => {
  if (isUndoingDelete.value || config.running || !canUndoDelete.value) return
  isUndoingDelete.value = true
  try {
    const next = await runAction(() =>
      CoreService.UndoDeleteConfigFile(coreType.value),
    )
    if (next) {
      canUndoDelete.value = false
      showNotification({ content: 'coreConfigFileRestored', type: 'alert-success' })
      await scanConfigFiles(false)
    }
  } catch {
    await checkCanUndoDelete()
  } finally {
    isUndoingDelete.value = false
  }
}

const saveChannel = async (rawChannel: string) => {
  if (isSavingChannel.value || rawChannel === currentChannel.value) return
  isSavingChannel.value = true
  try {
    await runAction(() =>
      CoreService.UpdateCoreSettings({
        coreType: coreType.value,
        channel: rawChannel,
      }),
    )
  } finally {
    isSavingChannel.value = false
  }
  void checkUpdate(false)
}

const saveRunArgs = async () => {
  if (isSavingRunArgs.value || config.running) return
  isSavingRunArgs.value = true
  try {
    await runAction(() =>
      CoreService.UpdateCoreSettings({
        coreType: coreType.value,
        runArgs: runArgsInput.value,
      }),
    )
    isRunArgsDirty.value = false
    syncActiveConfigFile()
  } finally {
    isSavingRunArgs.value = false
  }
}

const handleCoreStartResult = (next: CoreConfig | null) => {
  isRunArgsDirty.value = false
  if (next) {
    if (!next.running && next.coreLogError) {
      showNotification({ content: t('coreStartFailed'), type: 'alert-error', timeout: 5000 })
    } else if (next.running && next.clashApiUrl) {
      syncManagedBackendFromCore(next)
      void startBackendSession()
    }
  }
}

const startCore = async () => {
  if (isStarting.value || config.running) return
  isStarting.value = true
  try {
    handleCoreStartResult(
      await runAction(() => CoreService.StartCore(runArgsInput.value, coreType.value)),
    )
  } finally {
    isStarting.value = false
  }
}

const stopCore = async () => {
  if (isStopping.value || !config.running) return
  isStopping.value = true
  try {
    await runAction(() => CoreService.StopCore())
    stopBackendSession()
  } finally {
    isStopping.value = false
  }
}

const restartCore = async () => {
  if (isRestarting.value || !config.installed) return
  isRestarting.value = true
  try {
    handleCoreStartResult(
      await runAction(() => CoreService.RestartCore(runArgsInput.value, coreType.value)),
    )
  } finally {
    isRestarting.value = false
  }
}

const openCoreLog = async () => {
  if (isOpeningLog.value) return
  isOpeningLog.value = true
  try {
    await CoreService.OpenCoreLog(coreType.value)
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  } finally {
    isOpeningLog.value = false
  }
}

const downloadConfig = async () => {
  if (
    isDownloadingConfig.value ||
    isImportingConfig.value ||
    !configURLInput.value.trim() ||
    !saveTargetFileName.value.trim()
  )
    return
  isDownloadingConfig.value = true
  try {
    const targetFileName = saveTargetFileName.value.trim()
    await runAction(() =>
      CoreService.DownloadConfig(configURLInput.value, targetFileName, coreType.value),
    )
    isConfigURLDirty.value = false
    void scanConfigFiles(false)
    showNotification({ content: 'coreConfigDownloadSuccess', type: 'alert-success' })
  } finally {
    isDownloadingConfig.value = false
  }
}

const openConfigFilePicker = () => {
  configFileInput.value?.click()
}

const importConfig = async (event: Event) => {
  const input = event.currentTarget as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return
  isImportingConfig.value = true
  try {
    const text = await file.text()
    const targetFileName = saveTargetFileName.value.trim() || file.name
    await runAction(() =>
      CoreService.ImportConfig(text, targetFileName, coreType.value),
    )
    void scanConfigFiles(false)
    showNotification({ content: 'coreConfigImportSuccess', type: 'alert-success' })
  } finally {
    isImportingConfig.value = false
    input.value = ''
  }
}

const loadConfig = async (useActiveCore = false, forceInputs = false) => {
  isRefreshing.value = true
  try {
    const next = await runAction(
      () =>
        useActiveCore
          ? CoreService.GetConfig()
          : CoreService.GetConfigForType(coreType.value),
      coreType.value,
      forceInputs,
    )
    return Boolean(next)
  } catch {
    return false
  } finally {
    isRefreshing.value = false
  }
}

const saveBehavior = async (changedCoreType?: CoreType) => {
  if (isSavingBehavior.value) return
  if (changedCoreType === 'sing-box' && config.autoStartSingBox) {
    config.autoStartMihomo = false
  } else if (changedCoreType === 'mihomo' && config.autoStartMihomo) {
    config.autoStartSingBox = false
  }
  isSavingBehavior.value = true
  try {
    await runAction(() =>
      CoreService.UpdateCoreSettings({
        coreType: coreType.value,
        runAsAdmin: config.runAsAdmin,
        autoStart: config.autoStart,
        autoStartSingBox: config.autoStartSingBox,
        autoStartMihomo: config.autoStartMihomo,
        stopCoreOnExit: config.stopCoreOnExit,
        backendDebugLog: config.backendDebugLog,
      }),
    )
  } catch {
    await loadConfig(false, true)
  } finally {
    isSavingBehavior.value = false
  }
}

let activeCheckKey = ''

const checkUpdate = async (notifyError = true, force = false) => {
  const targetCoreType = coreType.value
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
      coreType.value === targetCoreType &&
      currentChannel.value === targetChannel
    ) {
      config.latestVersion = next.latestVersion
      config.updateAvailable = next.updateAvailable
      if (next.installedVersion) {
        config.installedVersion = next.installedVersion
      }
      if (next.version) {
        config.version = next.version
      }
    }
    return next
  } catch (error) {
    if (
      notifyError &&
      sequence === checkSequence &&
      coreType.value === targetCoreType &&
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
  try {
    const next = await runAction(() =>
      CoreService.DownloadCore(currentDownloadURL.value, coreType.value),
    )
    if (next) {
      showNotification({ content: 'coreDownloadSuccess', type: 'alert-success' })
    }
  } finally {
    isDownloading.value = false
  }
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
        showNotification({ content: 'upToDate', type: 'alert-success' })
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
  loadSavedSource()
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
      void checkUpdate(false)
    }
  })()

  unsubStateChange = Events.On('core:state-changed', handleCoreStateChanged)
  document.addEventListener('visibilitychange', handleVisibilityChange)
})

let unsubStateChange: (() => void) | undefined
let stateChangeTimer: ReturnType<typeof setTimeout> | undefined

const handleCoreStateChanged = () => {
  if (stateChangeTimer) {
    clearTimeout(stateChangeTimer)
  }
  stateChangeTimer = setTimeout(() => {
    stateChangeTimer = undefined
    void loadConfig()
  }, 50)
}

const handleVisibilityChange = () => {
  if (!document.hidden) {
    void loadConfig()
  }
}

watch(
  () => props.coreType,
  () => {
    activeRequestId += 1
    checkSequence += 1
    activeCheckKey = ''
    isChecking.value = false
    isRefreshing.value = false
    availableConfigFiles.value = []
    activeConfigFile.value = ''
    canUndoDelete.value = false
    Object.assign(config, emptyCoreConfig(props.coreType))
    isRunArgsDirty.value = false
    isConfigURLDirty.value = false
    loadSavedSource()
    void (async () => {
      if (await loadConfig(false, true)) {
        void scanConfigFiles(false)
        void checkCanUndoDelete()
        void checkUpdate(false)
      }
    })()
  },
)

onUnmounted(() => {
  activeRequestId += 1
  isRefreshing.value = false
  if (unsubStateChange) {
    unsubStateChange()
    unsubStateChange = undefined
  }
  if (stateChangeTimer) {
    clearTimeout(stateChangeTimer)
  }
  document.removeEventListener('visibilitychange', handleVisibilityChange)
})
</script>
