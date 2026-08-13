<template>
  <div class="flex flex-col gap-3 text-sm">
    <section class="bg-base-100 rounded-xl p-4">
      <div class="flex w-full flex-col gap-3">
        <div class="flex items-center justify-between gap-3">
          <div>
            <div class="setting-item-label">{{ $t('coreRun') }}</div>
          </div>
          <span
            class="badge badge-sm"
            :class="config.running ? 'badge-success' : 'badge-ghost'"
          >
            {{ config.running ? $t('coreRunning') : $t('coreStopped') }}
          </span>
        </div>

        <textarea
          v-model="runArgsInput"
          class="textarea textarea-sm min-h-20 w-full font-mono text-xs"
          :aria-label="$t('coreRunArgs')"
          :disabled="config.running || isStarting || isStopping || isRestarting"
        ></textarea>

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

      </div>
    </section>

    <section class="bg-base-100 rounded-xl p-4">
      <div class="flex w-full flex-col gap-3">
        <div class="setting-item-label">{{ $t('coreConfigDownload') }}</div>
        <input
          v-model="configURLInput"
          class="input input-sm w-full"
          type="url"
          :aria-label="$t('coreConfigURLPlaceholder')"
        />
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
        :aria-label="$t('coreDownloadURLPlaceholder')"
        @change="saveURL"
      />

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
    <section class="bg-base-100 rounded-xl p-4">
      <div class="flex w-full flex-col gap-1">
        <div class="setting-item-label mb-2">{{ $t('coreBehavior') }}</div>
        <label class="flex items-center justify-between gap-3 py-2">
          <span>{{ $t('coreRunAsAdmin') }}</span>
          <input v-model="config.runAsAdmin" class="toggle toggle-sm" type="checkbox" :disabled="isSavingBehavior" @change="saveBehavior" />
        </label>
        <label class="flex items-center justify-between gap-3 py-2">
          <span>{{ $t('coreAutoStart') }}</span>
          <input v-model="config.autoStart" class="toggle toggle-sm" type="checkbox" :disabled="isSavingBehavior" @change="saveBehavior" />
        </label>
        <label class="flex items-center justify-between gap-3 py-2">
          <span>{{ $t('coreAutoStartCore') }}</span>
          <input v-model="config.autoStartCore" class="toggle toggle-sm" type="checkbox" :disabled="isSavingBehavior" @change="saveBehavior" />
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
const isSavingBehavior = ref(false)

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

const saveBehavior = async () => {
  if (isSavingBehavior.value) return
  isSavingBehavior.value = true
  try {
    applyConfig(await CoreService.SaveBehavior(config.runAsAdmin, config.autoStart, config.autoStartCore))
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

onMounted(loadConfig)
</script>
