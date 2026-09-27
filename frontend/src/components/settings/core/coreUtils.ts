import { showNotification } from '@/helper/notification'
import type { CoreType } from './coreSources'

export const validateConfigFileName = (fileName: string, type: CoreType): boolean => {
  const name = fileName.trim()
  if (!name) {
    showNotification({ content: 'configNameRequired', type: 'alert-warning' })
    return false
  }
  if (/[<>:"/\\|?*]/.test(name) || name.includes('/') || name.includes('\\')) {
    showNotification({ content: 'configNameInvalidChars', type: 'alert-warning' })
    return false
  }
  const lower = name.toLowerCase()
  if (type === 'mihomo') {
    if (!lower.endsWith('.yaml') && !lower.endsWith('.yml')) {
      showNotification({ content: 'configExtMihomo', type: 'alert-warning' })
      return false
    }
  } else if (type === 'sing-box') {
    if (!lower.endsWith('.json')) {
      showNotification({ content: 'configExtSingbox', type: 'alert-warning' })
      return false
    }
  }
  return true
}

export const checkUnclosedQuotes = (args: string): boolean => {
  let inSingle = false
  let inDouble = false
  for (let i = 0; i < args.length; i++) {
    const ch = args[i]
    if (ch === '\\' && i + 1 < args.length) {
      i++
      continue
    }
    if (ch === '\'' && !inDouble) inSingle = !inSingle
    if (ch === '"' && !inSingle) inDouble = !inDouble
  }
  return inSingle || inDouble
}
