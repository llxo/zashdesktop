# zashdesktop

面向 Windows 裸核用户的 sing-box 桌面管理工具。zashdesktop 将核心管理、配置管理和 zashboard 前端整合到一个原生桌面应用中，减少浏览器快捷方式带来的常驻内存占用。

## 主要功能

- 自启动核心，并快速启动、停止和重启核心
- 系统托盘常驻，关闭窗口后核心仍可在后台运行
- 通过系统托盘菜单快速切换代理组
- 下载和管理核心配置
- 使用 zashboard 的成熟前端设计
- 后端逻辑借鉴 GUI.for.SingBox
- 基于 Wails v3 和系统 WebView2，不捆绑额外浏览器

## 资源占用

| 状态 | 内存占用 | 行为 |
| --- | ---: | --- |
| 仅运行托盘 | 不超过 10 MB | 核心和托盘保持运行 |
| 打开管理窗口 | 约 100-300 MB | 使用系统 WebView2 显示前端 |
| 关闭管理窗口 | 立即释放窗口内存 | WebView2 被销毁，核心和托盘继续运行 |

相比使用 zashboard 浏览器快捷方式，zashdesktop 可以显著减少常驻的浏览器内存占用。

## 前端基线

桌面版前端基于 [zashboard](https://github.com/Zephyruso/zashboard) `3.18.0`，基线 commit 为 [`58eb346b2846d72edc376647d9935bd65556669e`](https://github.com/Zephyruso/zashboard/commit/58eb346b2846d72edc376647d9935bd65556669e)。

## 使用

### 构建环境

需要安装以下组件：

- Go
- Node.js 和 pnpm
- Wails v3
- WebView2 运行时

前端源码位于 `frontend/`，桌面构建使用系统字体，并将静态资源输出到 `frontend/dist`。

### 首次启动和核心初始化

首次启动时会进入面板初始化页面，用于填写 Clash API 或 sing-box API 的连接信息。初始化页面下方有“核心”入口，即使还没有配置面板 API，也可以点击它进入核心页面完成初始化。

核心页面可以在 `sing-box` 和 `mihomo` 之间切换，并提供以下操作：

- 查看核心和配置的安装状态与路径
- 下载或手动添加核心
- 下载核心配置
- 启动、停止和重启核心
- 设置命令行参数、核心自启动和程序自启动

完成核心和配置初始化后，再返回面板初始化页面填写 API 信息即可进入代理面板。

### 手动添加核心和配置

程序会从 `zashdesktop.exe` 所在目录读取核心和配置。以
`C:\DEV\zashdesktop` 为例，目录结构如下：

```text
C:\DEV\zashdesktop\
├─ mihomo\
│  ├─ mihomo.exe
│  └─ config.yaml
├─ sing-box\
│  ├─ sing-box.exe
│  └─ config.json
└─ profiles.json
```

可以按照下面的规则手动复制文件：

1. 将 Windows x64 版 mihomo 核心复制到 `mihomo\mihomo.exe`，配置文件复制到 `mihomo\config.yaml`。
2. 将 Windows x64 版 sing-box 核心复制到 `sing-box\sing-box.exe`，配置文件复制到 `sing-box\config.json`。
3. 启动 zashdesktop，在核心设置中选择对应的核心类型；程序会自动识别对应目录中的核心和配置。
4. 确认核心可以正常启动后，再按需开启核心自启动。

核心文件名和配置文件名需要保持不变，配置文件也要放在对应核心目录中。两个核心可以同时保留，运行时选择其中一个作为当前核心。

### 构建程序

在 Windows PowerShell 中执行：

```powershell
.\build.ps1
```

构建脚本会生成 Wails 前端绑定、构建前端资源和生成 Windows 资源，最终输出：

```text
build/bin/zashdesktop.exe
```

### 命令行参数

```text
zashdesktop.exe --api-url http://127.0.0.1:9090 --api-secret secret --api-type clash
zashdesktop.exe --start-hidden
zashdesktop.exe --no-tray
```

| 参数 | 说明 |
| --- | --- |
| `--api-url` | Clash API 地址 |
| `--api-secret` | Clash API 密钥 |
| `--api-type` | API 类型，可选 `clash` 或 `sing-box` |
| `--start-hidden` | 启动后直接进入系统托盘 |
| `--no-tray` | 禁用系统托盘 |

## 后续计划

继续完善托盘右键菜单，增加以下快捷操作：

- 启动、停止和重启核心
