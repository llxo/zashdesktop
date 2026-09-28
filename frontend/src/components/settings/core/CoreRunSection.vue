<template>
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
          :disabled="config.running || isStarting || isStopping || isRestarting || isSavingRunArgs"
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
            :disabled="
              isStarting || isStopping || isRestarting || !config.installed || isOtherCoreRunning
            "
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
            class="btn btn-sm text-error min-w-16"
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
            :disabled="
              isStarting || isStopping || isRestarting || !config.installed || isOtherCoreRunning
            "
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
</template>

<script setup lang="ts">
import * as CoreService from '../../../../bindings/zashdesktop/coreservice'
import type { CoreConfig } from '../../../../bindings/zashdesktop/models'
import { showNotification } from '@/helper/notification'
import {
  ArrowPathIcon,
  DocumentTextIcon,
  PlayIcon,
  StopIcon,
} from '@heroicons/vue/24/outline'
import { computed, ref, watch } from 'vue'
import type { CoreType } from './coreSources'
import { checkUnclosedQuotes } from './coreUtils'

const props = defineProps<{
  coreType: CoreType
  config: CoreConfig
}>()

const emit = defineEmits<{
  (e: 'update:config', config: CoreConfig): void
}>()

const runArgsInput = ref(props.config.runArgs || '')
const isRunArgsDirty = ref(false)

const isStarting = ref(false)
const isStopping = ref(false)
const isRestarting = ref(false)
const isSavingRunArgs = ref(false)
const isOpeningLog = ref(false)

const defaultRunArgsPlaceholder = computed(() =>
  props.coreType === 'mihomo' ? '-d . -f "config.yaml"' : 'run -c "config.json" -D .',
)

const isOtherCoreRunning = computed(
  () => Boolean(props.config.runningCore && props.config.runningCore !== props.coreType),
)

const showCoreLogError = computed(() => !props.config.running && Boolean(props.config.coreLogError))

watch(
  () => props.config.runArgs,
  (newVal) => {
    if (!isRunArgsDirty.value) {
      runArgsInput.value = newVal || ''
    }
  },
)

watch(
  () => props.coreType,
  () => {
    runArgsInput.value = props.config.runArgs || ''
    isRunArgsDirty.value = false
  },
)

const saveRunArgs = async () => {
  if (isSavingRunArgs.value) return
  if (checkUnclosedQuotes(runArgsInput.value)) {
    showNotification({ content: 'runArgsUnclosedQuote', type: 'alert-warning' })
    return
  }
  isSavingRunArgs.value = true
  try {
    const updated = await CoreService.UpdateCoreSettings({
      coreType: props.coreType,
      runArgs: runArgsInput.value,
    })
    isRunArgsDirty.value = false
    if (updated) {
      emit('update:config', updated)
    }
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  } finally {
    isSavingRunArgs.value = false
  }
}

const startCore = async () => {
  if (isStarting.value) return
  if (checkUnclosedQuotes(runArgsInput.value)) {
    showNotification({ content: 'runArgsUnclosedQuote', type: 'alert-warning' })
    return
  }
  isStarting.value = true
  try {
    const next = await CoreService.StartCore(runArgsInput.value, props.coreType)
    isRunArgsDirty.value = false
    if (next && !next.running && next.coreLogError) {
      showNotification({ content: 'coreStartFailed', type: 'alert-error', timeout: 5000 })
    }
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  } finally {
    isStarting.value = false
  }
}

const stopCore = async () => {
  if (isStopping.value || !props.config.running) return
  isStopping.value = true
  try {
    await CoreService.StopCore()
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  } finally {
    isStopping.value = false
  }
}

const restartCore = async () => {
  if (isRestarting.value || !props.config.installed) return
  if (checkUnclosedQuotes(runArgsInput.value)) {
    showNotification({ content: 'runArgsUnclosedQuote', type: 'alert-warning' })
    return
  }
  isRestarting.value = true
  try {
    const next = await CoreService.RestartCore(runArgsInput.value, props.coreType)
    isRunArgsDirty.value = false
    if (next && !next.running && next.coreLogError) {
      showNotification({ content: 'coreStartFailed', type: 'alert-error', timeout: 5000 })
    }
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  } finally {
    isRestarting.value = false
  }
}

const openCoreLog = async () => {
  if (isOpeningLog.value) return
  isOpeningLog.value = true
  try {
    await CoreService.OpenCoreLog(props.coreType)
  } catch (error) {
    showNotification({ content: String(error), type: 'alert-error', timeout: 0 })
  } finally {
    isOpeningLog.value = false
  }
}
</script>
