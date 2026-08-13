<template>
  <div class="flex flex-col gap-3 text-sm">
    <section class="bg-base-100 rounded-xl p-4">
      <div class="flex w-full flex-col gap-3">
        <div class="flex items-center justify-between gap-3">
          <div>
            <div class="setting-item-label">{{ $t('coreRun') }}</div>
            <div class="text-base-content/55 mt-1 text-xs">{{ $t('coreRunTip') }}</div>
          </div>
          <span
            class="badge badge-sm"
            :class="config.running ? 'badge-success' : 'badge-ghost'"
          >
            {{ config.running ? $t('coreRunning') : $t('coreStopped') }}
          </span>
        </div>

        <div class="text-base-content/55 text-xs break-all">
          {{ $t('corePath') }}: {{ config.corePath || 'sing-box.exe' }}
        </div>
        <label class="flex flex-col gap-1">
          <span class="text-base-content/70 text-xs">{{ $t('coreRunArgs') }}</span>
          <textarea
            v-model="runArgsInput"
            class="textarea textarea-sm min-h-20 w-full font-mono text-xs"
            :placeholder="$t('coreRunArgsPlaceholder')"
            :disabled="config.running || isStarting || isStopping || isRestarting"
          ></textarea>
        </label>
        <div class="text-base-content/55 text-xs">{{ $t('coreRunArgsTip') }}</div>

        <div class="flex flex-wrap gap-2">
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

        <div
          v-if="config.running"
          class="text-base-content/55 text-xs"
        >
          {{ $t('coreProcess') }}: {{ config.pid }}
        </div>
        <div class="text-base-content/55 text-xs break-all">
          {{ $t('coreLogPath') }}: {{ config.logPath || 'sing-box/sing-box.log' }}
        </div>
      </div>
    </section>

    <section class="bg-base-100 rounded-xl p-4">
      <div class="flex w-full flex-col gap-3">
        <div class="setting-item-label">{{ $t('coreConfigDownload') }}</div>
        <div class="text-base-content/55 text-xs">{{ $t('coreConfigDownloadTip') }}</div>
        <input
          v-model="configURLInput"
          class="input input-sm w-full"
          type="url"
          :placeholder="$t('coreConfigURLPlaceholder')"
        />
        <div class="text-base-content/55 text-xs break-all">
          {{ $t('coreConfigPath') }}: {{ config.configPath || 'sing-box/config.json' }}
        </div>
        <div class="flex flex-wrap items-center gap-2">
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
          <span
            v-if="config.configAvailable"
            class="text-success text-xs"
          >
            {{ $t('coreConfigAvailable') }}
          </span>
        </div>
      </div>
    </section>

    <section class="bg-base-100 rounded-xl p-4">
      <div class="flex w-full flex-col gap-3">
      <div class="setting-item-label">{{ $t('coreDownloadURL') }}</div>
      <input
        v-model="urlInput"
        class="input input-sm w-full"
        type="url"
        :placeholder="$t('coreDownloadURLPlaceholder')"
        @change="saveURL"
      />
      <div class="text-base-content/55 text-xs">{{ $t('coreDownloadURLTip') }}</div>

      <div class="grid grid-cols-1 gap-2 sm:grid-cols-3">
        <div class="rounded-box bg-base-200/50 p-2">
          <div class="text-base-content/55 text-xs">{{ $t('coreVersion') }}</div>
          <div class="font-medium">{{ version || $t('unknown') }}</div>
        </div>
        <div class="rounded-box bg-base-200/50 p-2">
          <div class="text-base-content/55 text-xs">{{ $t('coreChannel') }}</div>
          <div class="font-medium">
            {{ config.channel === 'test' ? $t('coreTestBuild') : $t('coreStableBuild') }}
          </div>
        </div>
        <div class="rounded-box bg-base-200/50 p-2">
          <div class="text-base-content/55 text-xs">{{ $t('coreInstallStatus') }}</div>
          <div class="font-medium">
            {{
              config.installed
                ? $t('coreInstalled')
                : $t('coreNotInstalled')
            }}
          </div>
        </div>
        <div class="rounded-box bg-base-200/50 p-2">
          <div class="text-base-content/55 text-xs">{{ $t('coreLatestVersion') }}</div>
          <div class="font-medium">{{ config.latestVersion || $t('unknown') }}</div>
        </div>
      </div>

      <div
        v-if="config.latestVersion"
        class="text-xs"
        :class="config.updateAvailable ? 'text-warning' : 'text-success'"
      >
        {{ config.updateAvailable ? $t('coreUpdateAvailable') : $t('coreUpToDate') }}
      </div>

      <div
        v-if="config.updatePending"
        class="text-warning text-xs"
      >
        {{ $t('coreUpdatePending') }}
      </div>

      <div class="flex flex-wrap gap-2">
        <button
          class="btn btn-primary btn-sm"
          :disabled="isSaving || isDownloading || !urlInput.trim()"
          @click="saveURL"
        >
          <span
            v-if="isSaving"
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
import { onMounted, reactive, ref } from 'vue'

const config = reactive<CoreConfig>({
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
  backupVersion: '',
  backupAvailable: false,
  pendingVersion: '',
  updatePending: false,
  runArgs: '',
  configURL: '',
  running: false,
  pid: 0,
  logPath: '',
  configPath: '',
  configAvailable: false,
})
const urlInput = ref('')
const runArgsInput = ref('')
const configURLInput = ref('')
const isSaving = ref(false)
const isChecking = ref(false)
const isDownloading = ref(false)
const isStarting = ref(false)
const isStopping = ref(false)
const isRestarting = ref(false)
const isSavingRunArgs = ref(false)
const isDownloadingConfig = ref(false)

const applyConfig = (next: CoreConfig) => {
  Object.assign(config, next)
  urlInput.value = next.urlTemplate
  runArgsInput.value = next.runArgs
  configURLInput.value = next.configURL
}

const saveRunArgs = async () => {
  if (isSavingRunArgs.value || config.running) return
  isSavingRunArgs.value = true
  try {
    applyConfig(await CoreService.SaveRunArgs(runArgsInput.value))
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
    applyConfig(await CoreService.StartCore(runArgsInput.value))
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
    applyConfig(await CoreService.RestartCore(runArgsInput.value))
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
  try {
    applyConfig(await CoreService.GetConfig())
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
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

onMounted(loadConfig)
</script>
