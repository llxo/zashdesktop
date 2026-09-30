<template>
  <div class="flex flex-col gap-3 text-sm">
    <!-- 核心行为 -->
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
              v-model="behaviorConfig.runAsAdmin"
              class="toggle"
              type="checkbox"
              :disabled="isSavingBehavior"
              @change="updateSetting('runAsAdmin')"
            />
          </div>
        </label>
        <label
          class="setting-item"
          :class="{ 'opacity-50 cursor-not-allowed': !behaviorConfig.isAdmin }"
          :title="!behaviorConfig.isAdmin ? $t('adminPrivilegeRequired') : undefined"
        >
          <span class="w-20 sm:w-24 shrink-0 text-sm font-medium whitespace-nowrap">
            {{ $t('coreAutoStart') }}
          </span>
          <div class="flex flex-1 justify-end">
            <input
              v-model="behaviorConfig.autoStart"
              class="toggle"
              type="checkbox"
              :disabled="isSavingBehavior || !behaviorConfig.isAdmin"
              @change="updateSetting('autoStart')"
            />
          </div>
        </label>
        <label class="setting-item">
          <span class="w-20 sm:w-24 shrink-0 text-sm font-medium whitespace-nowrap">
            {{ $t('autoStartSingBox') }}
          </span>
          <div class="flex flex-1 justify-end">
            <input
              v-model="behaviorConfig.autoStartSingBox"
              class="toggle"
              type="checkbox"
              :disabled="isSavingBehavior"
              @change="updateSetting('autoStartSingBox')"
            />
          </div>
        </label>
        <label class="setting-item">
          <span class="w-20 sm:w-24 shrink-0 text-sm font-medium whitespace-nowrap">
            {{ $t('autoStartMihomo') }}
          </span>
          <div class="flex flex-1 justify-end">
            <input
              v-model="behaviorConfig.autoStartMihomo"
              class="toggle"
              type="checkbox"
              :disabled="isSavingBehavior"
              @change="updateSetting('autoStartMihomo')"
            />
          </div>
        </label>
        <label class="setting-item">
          <span class="w-20 sm:w-24 shrink-0 text-sm font-medium whitespace-nowrap">
            {{ $t('stopCoreOnExit') }}
          </span>
          <div class="flex flex-1 justify-end">
            <input
              v-model="behaviorConfig.stopCoreOnExit"
              class="toggle"
              type="checkbox"
              :disabled="isSavingBehavior"
              @change="updateSetting('stopCoreOnExit')"
            />
          </div>
        </label>
        <label class="setting-item">
          <span class="w-20 sm:w-24 shrink-0 text-sm font-medium whitespace-nowrap">
            {{ $t('backendDebugLog') }}
          </span>
          <div class="flex flex-1 justify-end">
            <input
              v-model="behaviorConfig.backendDebugLog"
              class="toggle"
              type="checkbox"
              :disabled="isSavingBehavior"
              @change="updateSetting('backendDebugLog')"
            />
          </div>
        </label>
      </div>
    </section>

    <!-- 应用更新 -->
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
  </div>
</template>

<script setup lang="ts">
import * as CoreService from '../../../../bindings/zashdesktop/coreservice'
import type { AppUpdateInfo } from '../../../../bindings/zashdesktop/models'
import { showNotification } from '@/helper/notification'
import {
  ArrowDownCircleIcon,
  ArrowPathIcon,
  ArrowRightIcon,
} from '@heroicons/vue/24/outline'
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { Events } from '@wailsio/runtime'

type BehaviorField =
  | 'runAsAdmin'
  | 'autoStart'
  | 'autoStartSingBox'
  | 'autoStartMihomo'
  | 'stopCoreOnExit'
  | 'backendDebugLog'

let cachedBehavior: any = null

const behaviorConfig = reactive({
  runAsAdmin: cachedBehavior?.runAsAdmin ?? false,
  isAdmin: cachedBehavior?.isAdmin ?? false,
  autoStart: cachedBehavior?.autoStart ?? false,
  autoStartSingBox: cachedBehavior?.autoStartSingBox ?? false,
  autoStartMihomo: cachedBehavior?.autoStartMihomo ?? false,
  stopCoreOnExit: cachedBehavior?.stopCoreOnExit ?? true,
  backendDebugLog: cachedBehavior?.backendDebugLog ?? false,
})

const isSavingBehavior = ref(false)
const isCheckingAppUpdate = ref(false)
const isUpdatingApp = ref(false)
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

const appVersionLabel = computed(() => {
  const v = appVersion.value || __APP_VERSION__
  return v ? (v.startsWith('v') ? v : `v${v}`) : 'v0.0.0'
})

const loadBehaviorConfig = async () => {
  try {
    const config = await CoreService.GetCoreState()
    if (config) {
      behaviorConfig.runAsAdmin = config.runAsAdmin
      behaviorConfig.isAdmin = config.isAdmin
      behaviorConfig.autoStart = config.autoStart
      behaviorConfig.autoStartSingBox = config.autoStartSingBox
      behaviorConfig.autoStartMihomo = config.autoStartMihomo
      behaviorConfig.stopCoreOnExit = config.stopCoreOnExit
      behaviorConfig.backendDebugLog = config.backendDebugLog
      cachedBehavior = { ...behaviorConfig }
    }
  } catch {}
}

const updateSetting = async (key: BehaviorField) => {
  if (isSavingBehavior.value) return

  if (key === 'autoStart' && !behaviorConfig.isAdmin) {
    behaviorConfig.autoStart = false
    showNotification({ content: 'adminPrivilegeRequired', type: 'alert-warning' })
    return
  }

  if (key === 'autoStartSingBox' && behaviorConfig.autoStartSingBox) {
    behaviorConfig.autoStartMihomo = false
  } else if (key === 'autoStartMihomo' && behaviorConfig.autoStartMihomo) {
    behaviorConfig.autoStartSingBox = false
  }

  isSavingBehavior.value = true
  try {
    const patch: any = {
      coreType: 'sing-box',
      [key]: behaviorConfig[key],
    }
    if (key === 'autoStartSingBox' && behaviorConfig.autoStartSingBox) {
      patch.autoStartMihomo = false
    } else if (key === 'autoStartMihomo' && behaviorConfig.autoStartMihomo) {
      patch.autoStartSingBox = false
    }

    const updated = await CoreService.UpdateCoreSettings(patch)
    if (updated) {
      behaviorConfig.runAsAdmin = updated.runAsAdmin
      behaviorConfig.isAdmin = updated.isAdmin
      behaviorConfig.autoStart = updated.autoStart
      behaviorConfig.autoStartSingBox = updated.autoStartSingBox
      behaviorConfig.autoStartMihomo = updated.autoStartMihomo
      behaviorConfig.stopCoreOnExit = updated.stopCoreOnExit
      behaviorConfig.backendDebugLog = updated.backendDebugLog
      cachedBehavior = { ...behaviorConfig }
    }
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
    await loadBehaviorConfig()
  } finally {
    isSavingBehavior.value = false
  }
}

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

let unsubStateChange: (() => void) | undefined

onMounted(() => {
  void loadBehaviorConfig()
  void (async () => {
    try {
      const v = await CoreService.GetAppVersion()
      if (v) appVersion.value = v
      const info = await CoreService.GetAppUpdateInfo()
      if (info) Object.assign(appUpdateInfo, info)
    } catch {}
  })()
  unsubStateChange = Events.On('core:state-changed', () => {
    void loadBehaviorConfig()
  })
})

onUnmounted(() => {
  if (unsubStateChange) {
    unsubStateChange()
    unsubStateChange = undefined
  }
})
</script>
