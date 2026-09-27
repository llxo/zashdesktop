export type CoreType = 'sing-box' | 'mihomo'
export type CoreChannel = 'stable' | 'test'

export interface DownloadSource {
  label: string
  url: string
  channelURLs?: Partial<Record<CoreChannel, string>>
}

export const builtInDownloadSources: Record<CoreType, DownloadSource[]> = {
  'sing-box': [
    {
      label: 'llxo/sing-box-releases',
      url: 'https://github.com/llxo/sing-box-releases/releases/download/v{version}/sing-box-{version}-windows-amd64.zip',
      channelURLs: {
        stable:
          'https://github.com/llxo/sing-box-releases/releases/download/v{version}/sing-box-{version}-windows-amd64.zip',
        test: 'https://github.com/llxo/sing-box-releases/releases/download/v{version}/sing-box-{version}-windows-amd64.zip',
      },
    },
    {
      label: 'reF1nd/sing-box-releases',
      url: 'https://github.com/reF1nd/sing-box-releases/releases/download/v{version}/sing-box-{version}-windows-amd64.zip',
      channelURLs: {
        stable:
          'https://github.com/reF1nd/sing-box-releases/releases/download/v{version}/sing-box-{version}-windows-amd64.zip',
        test: 'https://github.com/reF1nd/sing-box-releases/releases/download/v{version}/sing-box-{version}-windows-amd64.zip',
      },
    },
    {
      label: 'SagerNet/sing-box',
      url: 'https://github.com/SagerNet/sing-box/releases/download/v{version}/sing-box-{version}-windows-amd64.zip',
      channelURLs: {
        stable:
          'https://github.com/SagerNet/sing-box/releases/download/v{version}/sing-box-{version}-windows-amd64.zip',
        test: 'https://github.com/SagerNet/sing-box/releases/download/v{version}/sing-box-{version}-windows-amd64.zip',
      },
    },
  ],
  mihomo: [
    {
      label: 'MetaCubeX/mihomo (官方)',
      url: 'https://github.com/MetaCubeX/mihomo/releases/download/v{version}/mihomo-windows-amd64-compatible-v{version}.zip',
      channelURLs: {
        stable:
          'https://github.com/MetaCubeX/mihomo/releases/download/v{version}/mihomo-windows-amd64-compatible-v{version}.zip',
        test: 'https://github.com/MetaCubeX/mihomo/releases/download/Prerelease-Alpha/mihomo-windows-amd64-compatible-{version}.zip',
      },
    },
    {
      label: 'vernesong/mihomo (Smart)',
      url: 'https://github.com/vernesong/mihomo/releases/download/Prerelease-Alpha/mihomo-windows-amd64-v2-go120-{version}.zip',
      channelURLs: {
        stable:
          'https://github.com/vernesong/mihomo/releases/download/Prerelease-Alpha/mihomo-windows-amd64-v2-go120-{version}.zip',
        test: 'https://github.com/vernesong/mihomo/releases/download/Prerelease-Alpha/mihomo-windows-amd64-v2-go120-{version}.zip',
      },
    },
  ],
}
