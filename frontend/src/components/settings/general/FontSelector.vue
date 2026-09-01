<template>
  <button
    ref="triggerRef"
    type="button"
    role="combobox"
    :class="['select custom-select cursor-pointer text-left', attrs.class]"
    :style="attrs.style"
    :disabled="disabled"
    :aria-expanded="isOpen"
    aria-haspopup="listbox"
    @click="toggle"
    @keydown="handleTriggerKeydown"
  >
    <span
      class="min-w-0 flex-1 truncate"
      :style="{ fontFamily: currentPreviewFont }"
    >
      {{ currentLabel }}
    </span>
  </button>

  <Teleport
    v-if="isMounted"
    to="#app-content"
  >
    <Transition name="floating-menu">
      <div
        v-if="isOpen"
        ref="panelRef"
        role="listbox"
        class="border-base-border bg-base-100 text-base-content fixed z-[100000] flex flex-col overflow-hidden rounded-lg border shadow-lg w-64"
        :style="panelStyle"
        @keydown.stop="handlePanelKeydown"
      >
        <!-- 搜索输入框 (固定在顶部) -->
        <div class="border-base-border bg-base-100 shrink-0 border-b p-1.5">
          <input
            ref="searchInputRef"
            v-model="searchQuery"
            type="text"
            class="input input-xs input-bordered w-full"
            :placeholder="$t('searchFonts')"
            @keydown.stop
          />
        </div>

        <!-- 字体列表容器 (独立滚动) -->
        <div class="max-h-60 overflow-y-auto p-1 scrollbar-thin">
          <template v-if="filteredGroups.length === 0">
            <div class="text-base-content/40 px-2 py-4 text-center text-xs">
              {{ $t('noContent') || '无匹配字体' }}
            </div>
          </template>

          <template
            v-for="group in filteredGroups"
            :key="group.name"
          >
            <div class="text-base-content/45 px-2 pt-2.5 pb-1 text-xs font-medium first:pt-1">
              {{ group.name }}
            </div>

            <div
              v-for="item in group.items"
              :key="item.value || 'default'"
              role="option"
              :aria-selected="isSelected(item.value)"
              class="floating-menu-option flex items-center justify-between gap-2"
              :class="[
                isSelected(item.value) ? 'text-primary font-medium' : '',
              ]"
              @click="selectFont(item.value)"
            >
              <div class="min-w-0 flex-1 truncate">
                <span
                  class="text-sm"
                  :style="{ fontFamily: getPreviewFontFamily(item.value) }"
                >
                  {{ item.label }}
                </span>
                <span
                  v-if="item.value && item.value !== item.label"
                  class="text-base-content/40 ml-1.5 text-xs font-normal"
                >
                  {{ item.value }}
                </span>
              </div>
              <CheckIcon
                v-if="isSelected(item.value)"
                class="h-4 w-4 shrink-0"
              />
            </div>
          </template>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup lang="ts">
import { GetSystemFonts } from '@/../bindings/zashdesktop/coreservice'
import { useFloatingMenu } from '@/composables/floatingMenu'
import { CheckIcon } from '@heroicons/vue/24/outline'
import { computed, nextTick, onMounted, ref, useAttrs, watch } from 'vue'
import { useI18n } from 'vue-i18n'

defineOptions({ inheritAttrs: false })

const props = withDefaults(
  defineProps<{
    disabled?: boolean
  }>(),
  {
    disabled: false,
  },
)

const model = defineModel<string>('value', {
  default: '',
})

const { t } = useI18n()
const attrs = useAttrs()
const triggerRef = ref<HTMLButtonElement>()
const panelRef = ref<HTMLDivElement>()
const searchInputRef = ref<HTMLInputElement>()
const isMounted = ref(false)
const isOpen = ref(false)
const searchQuery = ref('')
const systemFonts = ref<string[]>([])

const { panelStyle } = useFloatingMenu(triggerRef, panelRef, isOpen, {
  minimumWidth: 256,
  maximumHeight: 340,
})

const PRESET_FONTS = [
  { label: '系统默认', value: '', en: 'System Default' },
  { label: '微软雅黑', value: 'Microsoft YaHei', en: 'Microsoft YaHei' },
  { label: 'Segoe UI', value: 'Segoe UI', en: 'Segoe UI' },
  { label: 'Segoe UI Variable', value: 'Segoe UI Variable Text', en: 'Segoe UI Variable' },
  { label: '鸿蒙黑体', value: 'HarmonyOS Sans SC', en: 'HarmonyOS Sans' },
  { label: '苹方', value: 'PingFang SC', en: 'PingFang SC' },
  { label: '思源黑体', value: 'Source Han Sans SC', en: 'Source Han Sans' },
  { label: '思源宋体', value: 'Source Han Serif SC', en: 'Source Han Serif' },
  { label: '霞鹜文楷', value: 'LXGW WenKai', en: 'LXGW WenKai' },
  { label: '得意黑', value: 'Smiley Sans', en: 'Smiley Sans' },
  { label: 'OPPO Sans', value: 'OPPO Sans', en: 'OPPO Sans' },
  { label: 'MiSans', value: 'MiSans', en: 'MiSans' },
  { label: 'Cascadia Code', value: 'Cascadia Code', en: 'Cascadia Code' },
  { label: 'JetBrains Mono', value: 'JetBrains Mono', en: 'JetBrains Mono' },
  { label: 'Fira Code', value: 'Fira Code', en: 'Fira Code' },
  { label: 'Consolas', value: 'Consolas', en: 'Consolas' },
  { label: '宋体', value: 'SimSun', en: 'SimSun' },
  { label: '楷体', value: 'KaiTi', en: 'KaiTi' },
]

const loadFonts = async () => {
  try {
    const fonts = await GetSystemFonts()
    if (fonts && Array.isArray(fonts)) {
      systemFonts.value = fonts
    }
  } catch (err) {
    console.warn('Failed to get system fonts:', err)
  }
}

onMounted(() => {
  isMounted.value = true
  loadFonts()
})

const getPreviewFontFamily = (fontName: string) => {
  if (!fontName || fontName === 'default') {
    return 'system-ui, -apple-system, sans-serif'
  }
  return `'${fontName}', system-ui, sans-serif`
}

const currentPreviewFont = computed(() => getPreviewFontFamily(model.value))

const currentLabel = computed(() => {
  const current = (model.value || '').trim()
  if (!current || current === 'default' || current === 'SystemUI') {
    return t('systemDefault') || '系统默认'
  }
  const foundPreset = PRESET_FONTS.find((p) => p.value.toLowerCase() === current.toLowerCase())
  if (foundPreset) {
    return foundPreset.label
  }
  return current
})

const isSelected = (val: string) => {
  const current = (model.value || '').trim()
  const target = (val || '').trim()
  if (!current || current === 'default' || current === 'SystemUI') {
    return !target || target === 'default' || target === 'SystemUI'
  }
  return current.toLowerCase() === target.toLowerCase()
}

type FontItem = {
  label: string
  value: string
  en?: string
}

type FontGroup = {
  name: string
  items: FontItem[]
}

const allGroups = computed<FontGroup[]>(() => {
  const installedLowerSet = new Set(systemFonts.value.map((f) => f.toLowerCase()))

  // 常用推荐：系统默认 + 系统中已安装的预设字体 + Windows 基础字体
  const recommendedItems: FontItem[] = []
  const recommendedValues = new Set<string>()

  for (const preset of PRESET_FONTS) {
    if (
      preset.value === '' ||
      preset.value === 'Microsoft YaHei' ||
      preset.value === 'Segoe UI' ||
      installedLowerSet.has(preset.value.toLowerCase())
    ) {
      recommendedItems.push(preset)
      if (preset.value) {
        recommendedValues.add(preset.value.toLowerCase())
      }
    }
  }

  const groups: FontGroup[] = [
    {
      name: t('recommendedFonts') || '常用推荐',
      items: recommendedItems,
    },
  ]

  // 当前自定义：如果当前选中的字体既不是空，也不在推荐列表中
  const currentVal = (model.value || '').trim()
  if (currentVal && !recommendedValues.has(currentVal.toLowerCase())) {
    groups.push({
      name: t('currentCustomFont') || '当前自定义',
      items: [{ label: currentVal, value: currentVal }],
    })
  }

  // 全部系统字体：排除已在推荐中展示的
  const systemItems: FontItem[] = []
  for (const fontName of systemFonts.value) {
    if (!recommendedValues.has(fontName.toLowerCase())) {
      systemItems.push({ label: fontName, value: fontName })
    }
  }

  if (systemItems.length > 0) {
    groups.push({
      name: t('systemFonts') || '系统已安装字体',
      items: systemItems,
    })
  }

  return groups
})

const filteredGroups = computed(() => {
  const q = searchQuery.value.trim().toLowerCase()
  if (!q) return allGroups.value

  const result: FontGroup[] = []
  for (const group of allGroups.value) {
    const matched = group.items.filter((item) => {
      const matchLabel = item.label.toLowerCase().includes(q)
      const matchVal = item.value.toLowerCase().includes(q)
      const matchEn = item.en?.toLowerCase().includes(q)
      return matchLabel || matchVal || matchEn
    })
    if (matched.length > 0) {
      result.push({
        name: group.name,
        items: matched,
      })
    }
  }
  return result
})

const selectFont = (fontValue: string) => {
  model.value = fontValue
  isOpen.value = false
  triggerRef.value?.focus()
}

const toggle = () => {
  if (props.disabled) return
  isOpen.value = !isOpen.value
}

const handleTriggerKeydown = (event: KeyboardEvent) => {
  if (event.key === 'Enter' || event.key === ' ' || event.key === 'ArrowDown') {
    event.preventDefault()
    isOpen.value = true
  }
}

const handlePanelKeydown = (event: KeyboardEvent) => {
  if (event.key === 'Escape') {
    isOpen.value = false
    triggerRef.value?.focus()
  }
}

watch(isOpen, (val) => {
  if (val) {
    searchQuery.value = ''
    nextTick(() => {
      searchInputRef.value?.focus()
    })
  }
})
</script>
