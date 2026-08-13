<template>
  <div class="bg-base-100 rounded-xl p-4 text-sm">
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
          <div class="font-medium">{{ config.version || $t('unknown') }}</div>
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
                ? config.installedVersion || $t('coreInstalled')
                : $t('coreNotInstalled')
            }}
          </div>
        </div>
        <div class="rounded-box bg-base-200/50 p-2">
          <div class="text-base-content/55 text-xs">最新版本</div>
          <div class="font-medium">{{ config.latestVersion || $t('unknown') }}</div>
        </div>
      </div>

      <div
        v-if="config.latestVersion"
        class="text-xs"
        :class="config.updateAvailable ? 'text-warning' : 'text-success'"
      >
        {{ config.updateAvailable ? '有更新可用' : '当前已是最新版本' }}
      </div>

      <div class="text-base-content/55 text-xs break-all">
        {{ $t('corePath') }}: {{ config.corePath || 'sing-box.exe' }}
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
          :disabled="isSaving || isChecking || isDownloading || !config.version"
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
          检查更新
        </button>
        <button
          class="btn btn-sm"
          :disabled="isSaving || isChecking || isDownloading || !config.version"
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
          {{ config.updateAvailable ? '更新核心' : $t('downloadCore') }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import * as CoreService from '../../../../bindings/sing-box-gui/coreservice'
import type { CoreConfig } from '../../../../bindings/sing-box-gui/models'
import { showNotification } from '@/helper/notification'
import { ArrowDownCircleIcon, ArrowDownTrayIcon, ArrowPathIcon } from '@heroicons/vue/24/outline'
import { onMounted, reactive, ref } from 'vue'

const config = reactive<CoreConfig>({
  urlTemplate: '',
  version: '',
  channel: '',
  corePath: '',
  installedVersion: '',
  installed: false,
  latestVersion: '',
  updateAvailable: false,
})
const urlInput = ref('')
const isSaving = ref(false)
const isChecking = ref(false)
const isDownloading = ref(false)

const applyConfig = (next: CoreConfig) => {
  Object.assign(config, next)
  urlInput.value = next.urlTemplate
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
  if (isChecking.value || !config.version) return
  isChecking.value = true
  try {
    applyConfig(await CoreService.CheckUpdate())
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
    applyConfig(await CoreService.DownloadCore())
    showNotification({ content: 'coreDownloadSuccess', type: 'alert-success' })
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  } finally {
    isDownloading.value = false
  }
}

onMounted(loadConfig)
</script>
