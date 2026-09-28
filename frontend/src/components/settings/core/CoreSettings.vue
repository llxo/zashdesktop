<template>
  <div class="flex flex-col gap-3 text-sm">
    <CoreRunSection
      :core-type="coreType"
      :config="config"
      @update:config="applyConfig"
    />
    <CoreVersionSection
      :core-type="coreType"
      :config="config"
      @update:config="applyConfig"
    />
    <CoreConfigSection
      :core-type="coreType"
      :config="config"
      @update:config="applyConfig"
    />
  </div>
</template>

<script setup lang="ts">
import * as CoreService from '../../../../bindings/zashdesktop/coreservice'
import type { CoreConfig } from '../../../../bindings/zashdesktop/models'
import { Events } from '@wailsio/runtime'
import { computed, onMounted, onUnmounted, reactive, watch } from 'vue'
import CoreConfigSection from './CoreConfigSection.vue'
import CoreRunSection from './CoreRunSection.vue'
import CoreVersionSection from './CoreVersionSection.vue'
import type { CoreType } from './coreSources'

const props = defineProps<{
  coreType: CoreType
}>()

const coreType = computed(() => props.coreType)

const emptyCoreConfig = (type: CoreType): CoreConfig => ({
  coreType: type,
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
  configFileName: type === 'mihomo' ? 'config.yaml' : 'config.json',
  running: false,
  runningCore: '',
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

const coreConfigCache: Record<string, CoreConfig> = {}

const config = reactive<CoreConfig>({
  ...(coreConfigCache[props.coreType] || emptyCoreConfig(props.coreType)),
})

let activeRequestId = 0
let unsubStateChange: (() => void) | undefined
let stateChangeTimer: ReturnType<typeof setTimeout> | undefined

const applyConfig = (next: CoreConfig) => {
  if (!next.latestVersion && config.latestVersion && next.coreType === config.coreType) {
    next.latestVersion = config.latestVersion
    next.updateAvailable = config.updateAvailable
  }
  Object.assign(config, next)
  coreConfigCache[config.coreType] = { ...config }
}

const loadConfig = async () => {
  const reqId = ++activeRequestId
  const targetCore = coreType.value
  try {
    const next = await CoreService.GetConfigForType(targetCore)
    if (reqId === activeRequestId && targetCore === coreType.value && next) {
      applyConfig(next)
      return true
    }
    return false
  } catch {
    return false
  }
}

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
  (nextType, prevType) => {
    if (nextType === prevType) return
    activeRequestId += 1
    const cached = coreConfigCache[nextType]
    if (cached) {
      Object.assign(config, cached)
    } else {
      Object.assign(config, emptyCoreConfig(nextType))
    }
    void loadConfig()
  },
)

onMounted(() => {
  void loadConfig()
  unsubStateChange = Events.On('core:state-changed', handleCoreStateChanged)
  document.addEventListener('visibilitychange', handleVisibilityChange)
})

onUnmounted(() => {
  activeRequestId += 1
  if (unsubStateChange) {
    unsubStateChange()
    unsubStateChange = undefined
  }
  if (stateChangeTimer) {
    clearTimeout(stateChangeTimer)
    stateChangeTimer = undefined
  }
  document.removeEventListener('visibilitychange', handleVisibilityChange)
})
</script>
