<template>
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
            :disabled="isDownloadingConfig || isImportingConfig"
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
            :disabled="isDownloadingConfig || isImportingConfig"
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

<script setup lang="ts">
import * as CoreService from '../../../../bindings/zashdesktop/coreservice'
import type { CoreConfig } from '../../../../bindings/zashdesktop/models'
import { showNotification } from '@/helper/notification'
import {
  ArrowDownTrayIcon,
  ArrowUpTrayIcon,
  ArrowUturnLeftIcon,
  TrashIcon,
} from '@heroicons/vue/24/outline'
import { computed, onMounted, reactive, ref, watch } from 'vue'
import type { CoreType } from './coreSources'
import { validateConfigFileName } from './coreUtils'

const props = defineProps<{
  coreType: CoreType
  config: CoreConfig
}>()

const defaultConfigFileName = computed(() =>
  props.coreType === 'mihomo' ? 'config.yaml' : 'config.json',
)
const configFileAccept = computed(() => (props.coreType === 'mihomo' ? '.yaml,.yml' : '.json'))

const saveTargetFileNameMap = reactive<Record<CoreType, string>>({
  'sing-box': 'config.json',
  'mihomo': 'config.yaml',
})

const saveTargetFileName = computed({
  get: () => saveTargetFileNameMap[props.coreType] || defaultConfigFileName.value,
  set: (val: string) => {
    saveTargetFileNameMap[props.coreType] = val
  },
})

const configURLInput = ref(props.config.configURL || '')
const isConfigURLDirty = ref(false)
const configFileInput = ref<HTMLInputElement | null>(null)

const isDownloadingConfig = ref(false)
const isImportingConfig = ref(false)
const isScanningConfigFiles = ref(false)
const isSelectingConfigFile = ref(false)
const isDeletingConfigFile = ref(false)
const isUndoingDelete = ref(false)
const canUndoDelete = ref(false)
const availableConfigFiles = ref<string[]>([])
const activeConfigFile = ref('')

const syncActiveConfigFile = () => {
  const target = props.config.configFileName || defaultConfigFileName.value
  if (availableConfigFiles.value.length === 0) {
    activeConfigFile.value = ''
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
    const files = await CoreService.ListConfigFiles(props.coreType)
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
  if (isSelectingConfigFile.value || !activeConfigFile.value) return
  isSelectingConfigFile.value = true
  try {
    await CoreService.SelectConfigFile(activeConfigFile.value, props.coreType)
  } catch (error) {
    syncActiveConfigFile()
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  } finally {
    isSelectingConfigFile.value = false
  }
}

const checkCanUndoDelete = async () => {
  try {
    canUndoDelete.value = await CoreService.CanUndoDeleteConfigFile(props.coreType)
  } catch {
    canUndoDelete.value = false
  }
}

const deleteActiveConfigFile = async () => {
  if (
    isDeletingConfigFile.value ||
    !activeConfigFile.value ||
    availableConfigFiles.value.length === 0
  )
    return
  isDeletingConfigFile.value = true
  try {
    await CoreService.DeleteConfigFile(activeConfigFile.value, props.coreType)
    canUndoDelete.value = true
    showNotification({ content: 'coreConfigFileDeleted', type: 'alert-success' })
    await scanConfigFiles(false)
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  } finally {
    isDeletingConfigFile.value = false
  }
}

const undoDeleteConfigFile = async () => {
  if (isUndoingDelete.value || !canUndoDelete.value) return
  isUndoingDelete.value = true
  try {
    await CoreService.UndoDeleteConfigFile(props.coreType)
    canUndoDelete.value = false
    showNotification({ content: 'coreConfigFileRestored', type: 'alert-success' })
    await scanConfigFiles(false)
  } catch (error) {
    await checkCanUndoDelete()
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  } finally {
    isUndoingDelete.value = false
  }
}

const downloadConfig = async () => {
  if (isDownloadingConfig.value || isImportingConfig.value) return
  const rawURL = configURLInput.value.trim()
  if (!/^https?:\/\//i.test(rawURL)) {
    showNotification({ content: 'invalidConfigURL', type: 'alert-warning' })
    return
  }
  const targetFileName = saveTargetFileName.value.trim()
  if (!validateConfigFileName(targetFileName, props.coreType)) {
    return
  }
  isDownloadingConfig.value = true
  try {
    await CoreService.DownloadConfig(rawURL, targetFileName, props.coreType)
    isConfigURLDirty.value = false
    void scanConfigFiles(false)
    showNotification({ content: 'coreConfigDownloadSuccess', type: 'alert-success' })
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
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

  if (file.size === 0) {
    showNotification({ content: 'configContentEmpty', type: 'alert-warning' })
    input.value = ''
    return
  }
  if (file.size > 20 * 1024 * 1024) {
    showNotification({ content: 'configContentTooLarge', type: 'alert-warning' })
    input.value = ''
    return
  }

  const originalFileName = file.name.trim()
  if (!validateConfigFileName(originalFileName, props.coreType)) {
    input.value = ''
    return
  }

  isImportingConfig.value = true
  try {
    const text = await file.text()
    await CoreService.ImportConfig(text, originalFileName, props.coreType)
    await scanConfigFiles(false)
    showNotification({ content: 'coreConfigImportSuccess', type: 'alert-success' })
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  } finally {
    isImportingConfig.value = false
    input.value = ''
  }
}

watch(
  () => props.config.configURL,
  (newURL) => {
    if (!isConfigURLDirty.value) {
      configURLInput.value = newURL || ''
    }
  },
)

watch(
  () => props.config.configFileName,
  () => {
    syncActiveConfigFile()
  },
)

watch(
  () => props.coreType,
  () => {
    configURLInput.value = props.config.configURL || ''
    isConfigURLDirty.value = false
    availableConfigFiles.value = []
    activeConfigFile.value = ''
    canUndoDelete.value = false
    void scanConfigFiles(false)
    void checkCanUndoDelete()
  },
)

onMounted(() => {
  void scanConfigFiles(false)
  void checkCanUndoDelete()
})

defineExpose({
  scanConfigFiles,
  checkCanUndoDelete,
  syncActiveConfigFile,
})
</script>
