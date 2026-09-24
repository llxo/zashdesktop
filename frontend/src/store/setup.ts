import { useStorage } from '@/helper/storage'
import type { Backend } from '@/types'
import { isEqual, omit } from 'lodash'
import { v4 as uuid } from 'uuid'
import { computed, ref } from 'vue'
import { sourceIPLabelList } from './settings'

// 旧版本的后端结构:没有 `type` 字段,且 sing-box 以附属通道 `singboxChannel` 存在。
type LegacySingboxChannel = {
  protocol?: string
  host?: string
  port?: string
  secret?: string
}
type LegacyBackend = Partial<Backend> & { singboxChannel?: LegacySingboxChannel }

// 一次性迁移:补全 `type`;把旧的 singboxChannel 拆分为独立的 sing-box 后端。
const migrateBackendList = (list: LegacyBackend[]): Backend[] => {
  const migrated: Backend[] = []

  for (const item of list) {
    const channel = item.singboxChannel
    const base = omit(item, 'singboxChannel') as Backend

    migrated.push({
      ...base,
      type: base.type ?? 'clash',
    })

    if (channel?.host) {
      migrated.push({
        type: 'singbox',
        protocol: channel.protocol || 'http',
        host: channel.host,
        port: channel.port || '9090',
        secondaryPath: '',
        password: channel.secret || '',
        uuid: uuid(),
        label: base.label ? `${base.label} (sing-box)` : undefined,
      })
    }
  }

  return migrated
}

export const backendList = useStorage<Backend[]>('setup/api-list', [])

if (backendList.value.some((item) => !item.type || 'singboxChannel' in item)) {
  backendList.value = migrateBackendList(backendList.value as LegacyBackend[])
}

export const MANAGED_BACKEND_LABEL = 'ZashCore'

if (backendList.value.length === 0) {
  const defaultUuid = uuid()
  backendList.value = [
    {
      uuid: defaultUuid,
      type: 'clash',
      protocol: 'http',
      host: '127.0.0.1',
      port: '9090',
      secondaryPath: '',
      password: '',
      label: MANAGED_BACKEND_LABEL,
    },
  ]
}

export const activeUuid = useStorage<string>('setup/active-uuid', '')

if (
  backendList.value.length > 0 &&
  (!activeUuid.value || !backendList.value.some((b) => b.uuid === activeUuid.value))
) {
  activeUuid.value = backendList.value[0].uuid
}

export const activeBackend = computed(() =>
  backendList.value.find((backend) => backend.uuid === activeUuid.value),
)

// 切换后端的唯一写入口。切换本身只是改一个 uuid,但后续的一切(会话重启、探测、
// 提示)都挂在 activeBackend 的 watch 上,散着写 activeUuid 就没有地方能收口。
export const setActiveBackend = (uuid: string) => {
  activeUuid.value = uuid
}

// 后端管理面板的形态。null = 关闭;list / create / edit 是同一个面板的三种视图,
// 而不是三个弹窗 —— 从列表点进编辑、保存后退回列表,都不该有弹窗开合的闪烁。
//
// 放在 store 而不是 composable:api 层遇到 401 要把编辑框直接摆到用户面前,而按
// eslint.config.ts 的分层约束,api 层只允许依赖 store/setup。
export type BackendManagerView =
  { mode: 'list' } | { mode: 'create' } | { mode: 'edit'; uuid: string }

export const backendManagerView = ref<BackendManagerView | null>(null)

export const openBackendManager = (view: BackendManagerView = { mode: 'list' }) => {
  backendManagerView.value = view
}

export const closeBackendManager = () => {
  backendManagerView.value = null
}

export const switchActiveBackend = (direction: 1 | -1) => {
  if (backendList.value.length < 2) {
    return null
  }

  const currentIndex = backendList.value.findIndex((backend) => backend.uuid === activeUuid.value)
  const startIndex = currentIndex >= 0 ? currentIndex : 0
  const nextIndex = (startIndex + direction + backendList.value.length) % backendList.value.length

  const nextBackend = backendList.value[nextIndex]

  if (!nextBackend) {
    return null
  }

  setActiveBackend(nextBackend.uuid)
  return nextBackend
}

export const addBackend = (backend: Omit<Backend, 'uuid'>) => {
  const currentEnd = backendList.value.find((end) => {
    return isEqual(omit(end, 'uuid'), backend)
  })

  if (currentEnd) {
    setActiveBackend(currentEnd.uuid)
    return currentEnd.uuid
  }

  const id = uuid()

  backendList.value.push({
    ...backend,
    uuid: id,
  })
  setActiveBackend(id)
  return id
}

export const updateBackend = (uuid: string, backend: Omit<Backend, 'uuid'>) => {
  const index = backendList.value.findIndex((end) => end.uuid === uuid)
  if (index !== -1) {
    backendList.value[index] = {
      ...backend,
      uuid,
    }
  }
}

export const removeBackend = (uuid: string) => {
  const wasActive = activeUuid.value === uuid

  backendList.value = backendList.value.filter((end) => end.uuid !== uuid)

  // 删掉的正是当前后端时,顺手落到剩下的第一个。否则 activeBackend 变成 undefined,
  // 路由守卫会当场把用户踢去 setup 页 —— 而他只是在管理面板里删了一条。
  if (wasActive) {
    setActiveBackend(backendList.value[0]?.uuid ?? '')
  }

  sourceIPLabelList.value.forEach((label) => {
    if (label.scope && label.scope.includes(uuid)) {
      label.scope = label.scope.filter((scope) => scope !== uuid)
      if (!label.scope.length) {
        delete label.scope
      }
    }
  })
}

export const syncManagedBackendFromCore = (coreConfig: {
  clashApiHost?: string
  clashApiPort?: string
  clashApiSecret?: string
}) => {
  const host = coreConfig.clashApiHost || '127.0.0.1'
  const port = coreConfig.clashApiPort || '9090'
  const password = coreConfig.clashApiSecret || ''

  // 1. 查找是否已有专属标签的后端
  let target = backendList.value.find((b) => b.label === MANAGED_BACKEND_LABEL)

  // 2. 如果没找到专属标签，检查是否有默认模板的 'Default' 项（或者纯本地无标签的初始项），直接升级迁移
  if (!target) {
    const defaultBackend = backendList.value.find(
      (b) => b.label === 'Default' || (!b.label && (b.host === '127.0.0.1' || b.host === 'localhost')),
    )
    if (defaultBackend) {
      target = defaultBackend
    }
  }

  // 3. 如果依然没有，新建一个专属标签后端
  if (!target) {
    const id = addBackend({
      type: 'clash',
      protocol: 'http',
      host,
      port,
      secondaryPath: '',
      password,
      label: MANAGED_BACKEND_LABEL,
    })
    setActiveBackend(id)
    return
  }

  // 4. 检查是否需要更新（端口、密码、地址、标签等）
  const needUpdate =
    target.host !== host ||
    target.port !== port ||
    target.password !== password ||
    target.label !== MANAGED_BACKEND_LABEL ||
    target.type !== 'clash'

  if (needUpdate) {
    updateBackend(target.uuid, {
      ...target,
      host,
      port,
      password,
      label: MANAGED_BACKEND_LABEL,
      type: 'clash',
    })
  }

  // 5. 确保如果当前没有激活的后端，或者当前处于专属后端，激活它
  if (!activeUuid.value || activeUuid.value === target.uuid) {
    setActiveBackend(target.uuid)
  }
}

