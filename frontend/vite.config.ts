import vue from '@vitejs/plugin-vue'
import vueJsx from '@vitejs/plugin-vue-jsx'
import { execSync } from 'child_process'
import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import { VitePWA } from 'vite-plugin-pwa'
import { version } from './package.json'

const getGitCommitId = (): string => {
  try {
    const commitMessage = execSync('git log -1 --pretty=%B', { encoding: 'utf8' }).trim()

    if (commitMessage.includes('chore(main): release')) {
      return ''
    }

    return execSync('git rev-parse --short HEAD', { encoding: 'utf8' }).trim()
  } catch (error) {
    console.warn('无法获取git commit ID:', error)
    return ''
  }
}

// Selects which fonts get bundled. One of:
//   all (default) | cdn | firasans | misans | pingfang | sarasa | none
// See src/assets/load-fonts.ts for what each value loads.
const font = process.env.FONT || 'all'

export default defineConfig(({ mode }) => {
  const isDesktop = mode === 'desktop'

  return {
    define: {
      __APP_VERSION__: JSON.stringify(version),
      __COMMIT_ID__: JSON.stringify(getGitCommitId()),
      __FONT__: JSON.stringify(isDesktop ? 'none' : font),
    },
    base: './',
    plugins: [
      vue(),
      vueJsx(),
      ...(!isDesktop
        ? [
            VitePWA({
              registerType: 'autoUpdate',
              includeAssets: ['favicon.svg', 'favicon-dark.svg'],
              workbox: {
                globPatterns: ['**/*.{js,css,html,ico,png,svg,woff2,webp,jpg,md}'],
                maximumFileSizeToCacheInBytes: 4 * 1024 * 1024,
              },
              manifest: {
                name: 'zashboard',
                short_name: 'zashboard',
                description: 'a dashboard using clash api',
                theme_color: '#000000',
                icons: [
                  {
                    src: './pwa-192x192.png',
                    sizes: '192x192',
                    type: 'image/png',
                    purpose: 'any',
                  },
                  {
                    src: './pwa-512x512.png',
                    sizes: '512x512',
                    type: 'image/png',
                    purpose: 'any',
                  },
                  {
                    src: './pwa-maskable-192x192.png',
                    sizes: '192x192',
                    type: 'image/png',
                    purpose: 'maskable',
                  },
                  {
                    src: './pwa-maskable-512x512.png',
                    sizes: '512x512',
                    type: 'image/png',
                    purpose: 'maskable',
                  },
                ],
              },
            }),
          ]
        : []),
    ],
    resolve: {
      alias: {
        '@': fileURLToPath(new URL('./src', import.meta.url)),
        net: fileURLToPath(new URL('./src/helper/netShim.ts', import.meta.url)),
      },
    },
    server: {
      host: '127.0.0.1',
      port: Number(process.env.WAILS_VITE_PORT) || 9245,
      strictPort: true,
    },
    build: isDesktop
      ? {
          rollupOptions: {
            external: ['/wails/runtime.js'],
          },
        }
      : undefined,
  }
})
