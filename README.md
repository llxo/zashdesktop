# zashdesktop

面向 Windows 裸核用户的 sing-box 桌面管理工具。zashdesktop 将核心管理、配置管理和 zashboard 前端整合到一个原生桌面应用中，减少浏览器快捷方式带来的常驻内存占用。

## 主要功能

- 自启动核心，并在面板或托盘右键快捷启动、停止和重启核心
- 启动核心时自动清理旧日志
- 系统托盘常驻，关闭窗口后核心仍可在后台运行
- 通过系统托盘菜单快速切换代理组
- 下载和管理核心配置
- 使用 zashboard 的成熟前端设计
- 后端逻辑借鉴 GUI.for.SingBox
- 基于 Wails v3 和系统 WebView2，不捆绑额外浏览器

## 资源占用

| 状态 | 内存占用 | 行为 |
| --- | ---: | --- |
| 仅运行托盘 | 不超过 20 MB | 核心和托盘保持运行 |
| 打开管理窗口 | 约 100-300 MB | 使用系统 WebView2 显示前端 |
| 关闭管理窗口 | 立即释放窗口内存 | WebView2 被销毁，核心和托盘继续运行 |

相比使用 zashboard 浏览器快捷方式，zashdesktop 可以显著减少常驻的浏览器内存占用。

## 前端基线

桌面版前端基于 [zashboard](https://github.com/Zephyruso/zashboard) `3.21.0`

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
- 下载核心
- 下载核心配置
- 启动、停止和重启核心
- 设置命令行参数、核心自启动和程序自启动

下载核心及配置，启动成功后，再返回面板初始化页面填写 API 信息即可进入代理面板。

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

### 启动报错的处理

以 sing-box 为例

- 查看 /sing-box/core.log 的具体报错
- 检查 /sing-box/config.json 是否有问题
- 检查 /sing-box/sing-box.exe 是否为对应版本
- 删除 /sing-box/cache.db

### 构建程序

在 Windows PowerShell 中执行：

```powershell
.\build.ps1
```

构建脚本会生成 Wails 前端绑定、构建前端资源和生成 Windows 资源，最终输出：

```text
build/bin/zashdesktop.exe
```

## 后端架构与代码解析 (Backend Architecture)

`zashdesktop` 后端采用 Go 语言与 **Wails v3** 桌面框架构建，针对 Windows 原生环境进行了深度系统级适配，代码按功能领域模块化整合为 8 个核心文件：

| 文件 | 功能领域 | 职责说明 |
| :--- | :--- | :--- |
| [`main.go`](file:///C:/DEV/workspace/zashdesktop/main.go) | 应用入口与窗口控制 | 程序入口 `main()`、命令行参数解析、Wails v3 实例生命周期、单实例互斥控制、原生窗口尺寸与位置记忆/恢复、WebView2 缓存清理。 |
| [`core_service.go`](file:///C:/DEV/workspace/zashdesktop/core_service.go) | 核心调度与服务绑定 | 前端绑定的 `CoreService` 服务，处理内核启停调度、状态监听、`profiles.json` 原子化持久化，以及全后端模块统一的 `debug.log` 诊断日志。 |
| [`core_config.go`](file:///C:/DEV/workspace/zashdesktop/core_config.go) | 内核配置管理 | 提供内核订阅下载（HTTP/HTTPS）、本地配置文件导入、文件名安全规范化、多配置文件目录扫描与无缝选择切换。 |
| [`core_release.go`](file:///C:/DEV/workspace/zashdesktop/core_release.go) | 核心版本与更新下载 | sing-box 与 mihomo 的 GitHub Release 版本探测、多镜像加速下载、SHA256 完整性校验、ZIP / TAR.GZ 跨格式解压以及内核可执行文件热替换。 |
| [`core_process.go`](file:///C:/DEV/workspace/zashdesktop/core_process.go) | 进程探测与优雅控制 | 基于 Win32 Toolhelp32 快照探测进程、管理外部残留内核、通过 `GenerateConsoleCtrlEvent` (`CTRL_BREAK_EVENT`) 实现内核优雅停机与文件锁检测。 |
| [`tray_proxy.go`](file:///C:/DEV/workspace/zashdesktop/tray_proxy.go) | 系统托盘与节点管理 | Windows 原生托盘图标与动态菜单渲染，定时轮询 Clash API 同步代理组/节点状态，支持托盘一键启停核心与切换当前节点。 |
| [`behavior.go`](file:///C:/DEV/workspace/zashdesktop/behavior.go) | 系统集成与特权行为 | Windows UAC 管理员提权检测、注册表 AppCompatFlags 兼容层、基于 XML 计划任务（`schtasks.exe`）的高权限开机自启、开始菜单快捷方式维护及系统代理探测。 |
| [`app_update.go`](file:///C:/DEV/workspace/zashdesktop/app_update.go) | 客户端自身热更新 | 检测 `zashdesktop` 自身 GitHub 最新版本、二进制下载与校验、运行中可执行文件安全热替换（支持自动回退）与 PowerShell 辅助无缝重启。 |

## 后续计划

- 优化管理窗口的启动速度

