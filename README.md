# OpenList 盘符挂载器

把 OpenList 里的每个顶层目录（存储）映射成 Windows 盘符：`影片 → Z:`、`剧集 → Y:`……
常驻系统托盘，记住你挂过哪些、分别用了哪个盘符、走哪条通道，下次开机自动恢复。

单文件 exe。两条挂载通道可**逐个目录**混用：

| | rclone 通道（默认） | WebDAV 通道 |
|---|---|---|
| 驱动 | WinFsp（需装一次） | 系统自带 `mrxdav.sys` |
| 大文件顺序读写 | 好，能跑满千兆 | 一般，20–60 MB/s |
| 视频拖动 / 随机读 | 好，分块预读 + VFS 缓存 | 差，会重新拉整段 |
| 原地修改文件 | 支持 | 支持 |
| 额外依赖 | WinFsp 驱动 + rclone（约 85MB） | 无 |
| 显示容量 | 可指定（`--vfs-disk-space-total-size`） | 取决于服务器是否上报 quota |

不确定选哪个就用 rclone，想零安装就切 WebDAV。

---

## 安装

**系统要求**：Windows 10 / 11 x64。无需安装运行时，单文件 exe（约 8 MB）。

1. 到 [Releases](../../releases) 下载 `OpenListMounter.exe`（或 `OpenListMounter-vX.Y.Z.zip`，
   内含 exe、图标和本文档）
2. 放到一个**自己有写权限**的目录，例如 `D:\Tools\OpenListMounter\`
   - 别放 `C:\Program Files\`：那里不可写，配置会自动回落到 `%APPDATA%`
   - 便携程序，不写注册表（除开机自启的 Run 键），删除目录即卸载
3. 双击运行。首次启动会请求管理员权限（UAC），**必须允许**——挂盘符、装驱动都要管理员
4. 托盘出现硬盘图标（蓝色 = 待命）

> 首次运行如果弹 SmartScreen「Windows 已保护你的电脑」，点「更多信息」→「仍要运行」。
> exe 未做代码签名。

## 使用

### 1. 配置服务器

右键托盘图标 →「打开管理页面」（首次启动会自动打开浏览器）。

| 字段 | 填什么 |
|---|---|
| WebDAV 地址 | `http://NAS-IP:5244/dav`。只写到端口也行，会自动补 `/dav` |
| 用户名 / 密码 | OpenList 的账号密码；开了二次验证的话，用 OpenList 里单独生成的 WebDAV 密码 |
| 通道 | 默认 rclone；想零安装选 WebDAV（对比见上表） |

点「保存并扫描」，程序列出该地址下**所有顶层目录**（一个目录 ≈ 一个存储）。

### 2. 挂载

扫描结果默认全部勾选，勾选 = 下次启动自动挂载、且当前正在挂载。

- 首次：勾选后点「挂载」，程序自动准备依赖
  - rclone 通道：下载 WinFsp 驱动（约 2.1 MB）→ 自动安装（需管理员）
  - WebDAV 通道：改三处注册表 + 启动 WebClient 服务（需管理员）
- rclone.exe（约 85 MB）在第一次挂载时自动下载到 exe 同目录，国内走南京大学镜像
- 每个目录独立分配盘符：从 `Z:` 往前推，之后**记住**不变
- 想手动指定：管理页每行的「盘符」列直接改；「容量」列填个显示值（如 `2T`）

### 3. 日常

| 操作 | 怎么做 |
|---|---|
| 挂 / 卸单个目录 | 右键托盘图标，点目录名切换勾选 |
| 全部断开 | 托盘右键 →「全部断开」 |
| 开机自动恢复 | 托盘右键 →「开机自启」打勾 |
| 看日志 | 管理页右下角有配置目录，里面的 `log.txt` |
| 退出程序 | 托盘右键 →「退出」，或管理页「退出程序」（会先卸载所有盘符） |

关闭管理页面**不会**退出程序，它常驻托盘。要真正退出走上面两条路径。

### 卸载

1. 托盘右键 →「全部断开」→「退出」
2. 关掉「开机自启」
3. 删掉 exe 所在目录
4. 可选清理：`%LOCALAPPDATA%\OpenListMounter\`（rclone VFS 缓存，可能几十 G）；
   装了 WinFsp 的话在「设置 → 应用」里卸载「WinFsp」

---

## 通道说明

### rclone 通道（默认）

- 需要 **WinFsp** 驱动（官方签名，控制面板可正常卸载）。程序检测到缺失时会在管理员权限下自动下载安装。
- rclone.exe 由程序自动下载，放在**程序所在目录**（配置目录，见文末「文件位置」）。**国内只有南京大学镜像**收录 rclone：
  - `https://mirrors.nju.edu.cn/rclone/`（首选）
  - `https://downloads.rclone.org/`（官方，备用）

  中科大、清华 TUNA、阿里云、腾讯云均**没有** rclone 镜像。
- 每个目录一个 `rclone mount` 子进程，独立盘符。挂载参数：

  ```
  --network-mode --vfs-cache-mode full --vfs-cache-max-size 50G
  --vfs-cache-max-age 24h --vfs-disk-space-total-size 10T
  --buffer-size 32M --vfs-read-chunk-size 16M --dir-cache-time 30s
  ```

- VFS 缓存放在 `%LOCALAPPDATA%\OpenListMounter\vfs-cache`（故意不跟着程序目录走：上限 50G，
  免得便携目录被拷到 U 盘、被同步盘打包时顺带带走一堆缓存），上限 50G，可用上面的参数调整。
- 容量栏显示 10T 是程序主动上报的占位值，不是真实剩余空间——OpenList 不上报 quota。

### WebDAV 通道

走 Windows 自带 WebDAV 客户端，零安装。首次需要管理员权限改三处系统设置：

| 限制 | 默认值 | 改后 |
|---|---|---|
| 只允许 HTTPS 的 Basic 认证 | `BasicAuthLevel = 1` | `= 2`，允许明文 HTTP |
| 单文件大小上限 | 50 MB | 4 GB |
| 目录属性上限 | 1 MB（大目录列不全） | 10 MB |

位置：`HKLM\SYSTEM\CurrentControlSet\Services\WebClient\Parameters`，同时把 WebClient 服务设为自动启动。

---

## 实测性能

千兆内网，OpenList 后端挂的是网盘（非本地磁盘）：

| 通道 | 顺序读 |
|---|---|
| rclone（WinFsp） | **106 MB/s** |
| WebDAV（系统重定向器） | 20–60 MB/s |

测量方式：从挂载盘符顺序读取 200 MB，用时 1.9 秒，接近千兆网理论上限（125 MB/s）。

注意：这个数字的上限还取决于 **OpenList 到你后端存储**的那一段，不只是局域网带宽。
如果你的后端是本地磁盘，能更接近网卡上限。

---

## 命令行参数

```bat
:: 直接写入服务器配置（密码用 DPAPI 加密后存盘，不出现在配置文件明文里）
OpenListMounter.exe --set-server "http://192.168.1.10:5244/dav" 用户名 密码

:: 开机自启用（注册表 Run 键会带这个参数）
OpenListMounter.exe --auto

:: 跳过单实例检查，用于「以管理员身份重启」时的进程交接
OpenListMounter.exe --replace

:: 不自动提权（排查问题时用）
OpenListMounter.exe --no-elevate
```

---

## 容量显示

**OpenList 读不到真实剩余空间**，这是源头限制，不是客户端问题：

- WebDAV 的 `DAV:quota-available-bytes` / `quota-used-bytes`（RFC 4331）它直接返回 `404`；
- 它自己的管理 API（`/api/admin/storage/list`、`/api/fs/list`）里也没有任何容量字段。

所以本程序在管理页给了一列「容量」，由你手动填一个显示值：

| 填写 | 效果 |
|---|---|
| `2T` | 盘符显示 2 TB |
| `500G` | 盘符显示 500 GB |
| `1.5T` | 支持小数 |
| 留空 | 默认 10 TB |

改完会自动重新挂载使生效（盘符会闪一下）。这是**显示值**，不随实际使用变化，
只是为了让资源管理器的容量栏看起来符合实际，而不是一个莫名其妙的 10T。

---

## 托盘图标颜色

| 颜色 | 含义 |
|---|---|
| 蓝 | 正常待命，没有挂载任何目录 |
| 绿 | 有目录已挂载 |
| 黄 | 正在挂载 / 断开 |
| 红 | 有目录挂载失败 |
| 灰 | 还没配置服务器 |

右键菜单里每个目录一行，勾选状态就是当前是否已挂载，点一下即切换。

---

## 盘符规则

- 首次扫描：从 `Z:` 往前自动分配
- 之后**记住**：目录名不变，就永远用同一个盘符
- 记住的盘符被别的程序占了，自动顺延到下一个空闲盘符
- 管理页里可以手动改某个目录的盘符和通道
- 勾选框 = 下次启动是否自动挂载。**扫描不会再把它改回勾选**，取消勾选就是真的取消
- 服务器上把目录删了/改名：盘符、容量、通道会记在 `config.json` 的 `remembered` 里，
  同名目录再出现时自动还原（最多记 80 条）

---

## 关于 S3

有人会问能不能走 S3。三个原因不建议：

1. **OpenList 没有 S3 端点**。它是聚合客户端，只对外提供 WebDAV / HTTP，没有 S3 网关。
2. 就算直连底层的 MinIO / OSS，客户端侧仍然要 WinFsp + rclone，和 rclone 通道**同一个驱动**，瓶颈没变。
3. S3 的文件系统语义是降级的：不能原地修改（改一个字节要重传整个对象）、重命名等于复制+删除、目录是 prefix 假象。当普通硬盘用容易踩坑。

大文件传输的真正提速来自 rclone 的分片预读和 VFS 缓存，这部分与后端是 WebDAV 还是 S3 无关。

---

## 已知限制

- **WebDAV 通道不适合大文件**：断网后资源管理器会卡顿，此时从托盘断开重挂。
- **只支持 Basic 认证**。OpenList 开了二次验证，要在 OpenList 里生成 WebDAV 专用密码填进来。
- **同一台服务器只能有一个账号**。想连同一 OpenList 的两个账号，WebDAV 通道会报 1219，得先断开旧连接。
- **目录名不能有 `#` `?` 等 URL 保留字符**。
- 自动提权有 3 分钟冷却：提权后仍补不齐系统配置（比如 WinFsp 下载超时）时会退回普通身份，
  冷却窗口内不再自动提权，避免和普通实例来回拉起、无限弹 UAC。手动点「以管理员身份重启」不受限制。
- rclone 通道下，进程被强杀可能留下盘符残留，重新挂载会自动处理。

---

## 文件位置

**便携**：所有文件都落在 **exe 所在目录**，整个目录拷到别的盘 / U 盘就能带走配置。

```
<exe 同目录>\config.json    配置（密码用 Windows DPAPI 加密，仅本机本用户可解）
<exe 同目录>\log.txt        运行日志，排查问题先看这个
<exe 同目录>\rclone.exe     rclone 主程序（rclone 通道用到时自动下载）
<exe 同目录>\rclone.conf    自动生成的 rclone 配置，勿手改
<exe 同目录>\config.json.bad-*  配置解析失败时的原件备份（看到它就说明配置坏过）
%LOCALAPPDATA%\OpenListMounter\vfs-cache\   rclone 读缓存（50G 上限，故意留在本地）
```

- exe 放在 `C:\Program Files\` 这类**不可写目录**时，会自动回落到 `%APPDATA%\OpenListMounter\`。
  实际生效的路径在管理页右下角「配置在 …」里能看到，鼠标悬停有完整路径。
- 从旧版本升级时，`%APPDATA%\OpenListMounter\config.json` 会被**复制**一份到新位置
  （不是移动，旧文件留着兜底），日志第一行会写明。
- 配置写入是原子的（临时文件 + 改名覆盖）；万一解析失败，坏文件会改名成
  `config.json.bad-<时间戳>` 留证，不会再被静默清空。
- 开机自启写的是当时的 exe 绝对路径。程序**每次启动都会校验**：路径和当前 exe 不一致就自动重写。

---

## 从源码构建

需要 Go 1.21+，无第三方依赖（纯标准库 + Win32 syscall）。

```bash
build.bat
```

产物输出到 **`build\`** 目录，里面就是你要拿走的东西：

```
build\OpenListMounter.exe   主程序（单文件，已内嵌图标）
build\openlist.ico          图标源文件，给快捷方式用
build\README.md             这份说明
```

手动构建等价于：

```bash
go build -ldflags="-s -w -H windowsgui" -o build\OpenListMounter.exe .
go run ./tools/iconpatch build\OpenListMounter.exe openlist.ico
```

第二条命令把 `openlist.ico` 作为资源写进 exe。编译器不会自动做这件事——Go 链接器对外部 COFF 的 `.rsrc` 段重定位会产生损坏的 PE（表现为 "Exec format error"），所以改成编译完成后直接追加一个资源节。

> 资源树注意：PE 资源目录是**三层**（根=类型层 → 名称层 → 语言层 → 数据项），
> 根目录本身就是类型层，**不要**在它下面再套一层类型目录，否则资源 ID 会被错位解析，
> `ExtractIconEx` 直接返回 0。目录头是 16 字节（不是 14）。

图标是程序内用代码重绘的（`icon.go`，按 SVG 原始坐标画 5x5 超采样），不依赖外部资源文件，
任意 DPI 下都清晰。`openlist.ico` 只用于 exe 资源段和快捷方式。

## 目录结构

```
main.go             入口、单实例、消息循环
app.go              状态机：目录发现、盘符分配、通道分派、挂载编排、提权重启
tray.go             托盘图标与右键菜单
icon.go             图标渲染（SVG 几何重绘，5x5 超采样）
rclone.go           rclone 下载、WinFsp 安装、mount 进程管理
mount.go            Win32 网络映射、WebClient 服务与注册表配置
webdav.go           PROPFIND 探测与目录枚举
web.go              内嵌 HTTP 服务与 API
web/index.html      管理页面（go:embed 打进二进制）
config.go           配置持久化（便携目录 / 原子写 / 坏文件留证）+ DPAPI
quit_test.go        退出流程回归测试（消息循环必须真的退出）
config_test.go      配置读写、坏文件隔离、目录记忆测试
winapi.go           Win32 API 声明
build.bat           一键构建，产物进 build/
tools/iconpatch/    PE 资源注入工具
tools/icontest/      用 ExtractIconEx 验证图标（走系统真实路径）
tools/resprobe/      用 FindResource 探测资源是否可被系统识别
tools/pedump/        dump PE 头与节表
tools/preview/       开发期假数据预览服务
```

## 依赖与致谢

程序本身**不打包**任何第三方二进制，需要时在用户机器上现场下载：

| 组件 | 用途 | 许可 |
|---|---|---|
| [WinFsp](https://winfsp.dev/) | rclone 通道的用户态文件系统驱动 | GPLv3 + FLOSS 例外 |
| [rclone](https://rclone.org/) | rclone 通道的实际挂载进程 | MIT |
| [OpenList](https://github.com/OpenListTeam/OpenList) | 服务端，程序只消费它的 WebDAV | AGPL-3.0（与本程序无代码关系） |

WinFsp 与 rclone 的许可、版权归各自作者所有，本项目只做调用与自动化安装。

## 许可

[MIT](LICENSE)
