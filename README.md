# MTL (My Tiny Launcher)

轻量级命令行 Minecraft 启动器，原生支持 mrpack 整合包，主打 **Fabric + 离线模式**。

MTL 是自用项目，不是成熟的发行版启动器。功能按实际需要逐步迭代，优先离线、轻量、少联网。

## ✨ 已实现功能

- **mrpack 整合包安装**：解析 `modrinth.index.json`，下载客户端文件、解压 overrides，并自动安装对应版本的 Fabric loader。
- **哈希校验与自动跳过**：下载前校验已有文件的 sha1 / sha512，哈希一致的直接跳过；重复执行安装不会重复下载。
- **实例隔离**：每个整合包一个独立实例目录，mod、配置、存档、库文件互不影响。
- **Java 运行时自动准备**：按版本要求从 Mojang 官方运行时清单下载并校验，缓存复用；也可用 `--java` 指定本机 Java。
- **原版资源缓存**：assets 索引与资源对象下载到全局缓存，多个实例共享。
- **内存配置**：支持自动推荐或手动指定 `-Xms / -Xmx`，并校验不低于 256M、不超过物理内存的 90%。
- **JVM / 游戏参数配置**：可写入实例配置，也可在命令行单次追加；JVM 参数与游戏参数严格分离。
- **Windows 高性能显卡调度**：为 Java 可执行文件写入 `HKCU` 显卡首选项，并追加 D3D 参数；设置失败只告警，不影响启动。
- **离线账号**：全局保存离线账号（用户名 + 离线 UUID），实例可绑定默认账号，`--account` 可临时切换；同一台机器可用多个账号进行局域网联机。
- **崩溃日志归档与诊断**：每次启动后自动归档新增崩溃日志，并用离线关键词规则给出 Mod 冲突提示。
- **整合包查看**：`mtl inspect` 可在安装前查看整合包信息，按需解压 overrides。

## ⚠️ 当前已知限制

- 仅支持 **Fabric** loader 的 mrpack，不支持 Forge / NeoForge / Quilt 的自动安装。
- 仅有离线账号体系，不接入微软正版登录；启动参数中的 access token 固定为 `0`，用户类型为 `legacy`。
- 高性能显卡调度仅 Windows 生效，其他平台静默跳过。
- 崩溃诊断是**静态关键词匹配**，只能覆盖常见模式；复杂崩溃、混淆堆栈或非 Mod 问题仍需人工查看原始日志。
- 重新安装 mrpack 不会删除“新包中已移除”的旧文件，需要手动清理。
- 没有图形界面、皮肤/披风、Mod 管理、自动更新等启动器周边功能。
- Linux arm64 没有 Mojang 官方 Java 运行时，需要 `--java` 指定本机 Java。

## 📦 前置要求

- **Go 1.22+**（仅编译需要；运行编译后的二进制不需要 Go）
- **网络**：首次安装和启动需要下载整合包文件、Java 运行时、Minecraft 资源
- 无需预装 Java；默认自动下载 Mojang 官方运行时，也可用 `--java` 指定

## 🔨 编译

```bash
go build ./cmd/mtl
```

Windows 下生成 `mtl.exe`，其他平台生成 `mtl`。

项目不依赖 CGO，可交叉编译：

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o mtl.exe ./cmd/mtl
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -o mtl     ./cmd/mtl
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -o mtl     ./cmd/mtl
```

> Windows PowerShell / cmd 中运行当前目录的二进制请写 `.\mtl.exe`；Git Bash 可用 `./mtl`。

## 🚀 快速上手

以下示例假设整合包为 `./test-pack.mrpack`，实例目录为 `./instances/demo`。

**1. 安装整合包**

```bash
./mtl install --dir ./instances/demo ./test-pack.mrpack
```

安装会下载整合包声明的客户端文件、解压 overrides，并安装对应 Fabric loader。耗时取决于网络，已存在且哈希正确的文件会自动跳过。

**2. 添加并绑定默认离线账号**

```bash
./mtl account add Steve
./mtl config set-default-account --dir ./instances/demo Steve
```

用户名规则：3–16 位字母、数字或下划线。实例只保存账号名，不保存密码或令牌。

**3. 启动游戏**

```bash
./mtl run --dir ./instances/demo
```

不传 `--account` 时，使用实例绑定的默认账号 `Steve`。首次启动会自动准备 Java 运行时和 assets。

**4. 临时切换账号（自己和自己联机）**

```bash
./mtl account add Alex
./mtl run --dir ./instances/demo --account Alex
```

两个客户端使用不同用户名即可获得不同离线 UUID。局域网服务端需要设置 `online-mode=false`，并且**不要**让两个客户端使用同一个用户名，否则 UUID 冲突。

> 账号优先级：`--account` > 实例 `default_account` > 显式 `--username`。三者都没有时会报错。

## 📖 全部命令参考

### mtl install

安装 mrpack 整合包到实例目录。

```bash
mtl install --dir <实例目录> [--concurrency N] [--skip-loader] <pack.mrpack>
```

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `--dir <目录>` | 是 | 实例目录，不存在时自动创建 |
| `--concurrency N` | 否 | 并发下载数，缺省为 4 |
| `--skip-loader` | 否 | 只下载整合包文件，不安装 Fabric loader |
| `<pack.mrpack>` | 是 | mrpack 文件路径 |

示例：

```bash
./mtl install --dir ./instances/demo ./test-pack.mrpack
```

### mtl run

启动指定实例。

```bash
mtl run --dir <实例目录> [--account <账号>] [--username <名字>] [--memory <内存>] \
        [--memory-auto] [--highperf-gpu|--no-highperf-gpu] [--java <路径>] \
        [--jvm-arg <参数>]... [--game-arg <参数>]... [--debug]
```

| 参数 | 说明 |
| --- | --- |
| `--dir <目录>` | 必填，实例目录 |
| `--account <账号>` | 使用已保存的离线账号，优先级最高 |
| `--username <名字>` | 临时离线用户名；仅在未指定 `--account` 且实例未绑定默认账号时生效 |
| `--memory <值>` | 手动指定最大堆，如 `4G`、`512M` |
| `--memory-auto` | 按物理内存自动计算最大堆；开启后忽略 `--memory` |
| `--highperf-gpu` | 本次强制开启高性能显卡调度，覆盖实例配置 |
| `--no-highperf-gpu` | 本次强制关闭高性能显卡调度，覆盖实例配置 |
| `--java <路径>` | 指定 java 可执行文件，跳过官方运行时下载 |
| `--jvm-arg <参数>` | 追加 JVM 参数，可重复 |
| `--game-arg <参数>` | 追加游戏参数，可重复 |
| `--debug` | 打印配置来源、完整启动命令和调试信息 |

示例：

```bash
./mtl run --dir ./instances/demo
./mtl run --dir ./instances/demo --account Alex
./mtl run --dir ./instances/demo --memory 6G --jvm-arg "-XX:+UseG1GC"
./mtl run --dir ./instances/demo --game-arg "--width" --game-arg "1280"
./mtl run --dir ./instances/demo --java "C:\Java\jdk-21\bin\java.exe" --debug
```

说明：

- JVM 参数顺序为：版本固定参数 → `-Xms / -Xmx` → `--jvm-arg`，因此自定义参数可以覆盖内存配置。
- `--game-arg` 只拼在主类之后，不会进入 JVM 参数区。
- 实例配置中的参数在前，命令行追加的参数在后。
- Java 优先级：`--java` > 实例配置 `java` > 全局缓存自动匹配下载。配置中的路径被手动删除时会自动重新匹配。
- 游戏成功运行结束后，本次使用的 Java 绝对路径会自动回填到实例配置（与已有值相同则跳过）；启动失败或中途崩溃不会修改配置。
- `Ctrl+C` 会转发给游戏进程；10 秒内未退出则强制结束。

### mtl inspect

查看 mrpack 内容，不安装。

```bash
mtl inspect [--extract <目录>] <pack.mrpack>
```

| 参数 | 说明 |
| --- | --- |
| `--extract <目录>` | 同时把 overrides 解压到指定目录 |
| `<pack.mrpack>` | 必填，mrpack 文件路径 |

示例：

```bash
./mtl inspect ./test-pack.mrpack
./mtl inspect --extract ./tmp/overrides ./test-pack.mrpack
```

### mtl account

管理全局离线账号。

```bash
mtl account list
mtl account add <name>
mtl account rm <name>
```

| 子命令 | 说明 |
| --- | --- |
| `list` | 列出全部离线账号及其 UUID |
| `add <name>` | 添加账号；已存在时不会重复添加 |
| `rm <name>` | 删除账号 |

示例：

```bash
./mtl account list
./mtl account add Steve
./mtl account rm Steve
```

说明：

- 用户名仅允许 3–16 位字母、数字或下划线。
- 账号文件是全局的，多个实例共享。
- 删除仍被实例绑定的账号后，该实例启动会报错，需要重新绑定或添加账号。

### mtl config

管理实例配置，配置文件为 `<实例目录>/.mtl/config.json`。

```bash
mtl config show --dir <实例目录>
mtl config set-memory <auto|manual> [--value <内存>] --dir <实例目录>
mtl config set-highperf-gpu <true|false> --dir <实例目录>
mtl config set-default-account --dir <实例目录> <账号名>
mtl config add-jvm-arg --dir <实例目录> <参数>
mtl config remove-jvm-arg --dir <实例目录> <参数>
mtl config add-game-arg --dir <实例目录> <参数>
mtl config remove-game-arg --dir <实例目录> <参数>
mtl config reset --dir <实例目录>
```

| 子命令 | 说明 |
| --- | --- |
| `show` | 打印当前实例配置 |
| `set-memory auto` | 使用自动内存模式 |
| `set-memory manual --value 4G` | 使用手动内存，`--value` 必填 |
| `set-highperf-gpu true` | 开启实例级高性能显卡调度（仅 Windows 生效） |
| `set-highperf-gpu false` | 关闭实例级高性能显卡调度 |
| `set-default-account <账号名>` | 绑定实例默认离线账号；只保存名字，不校验账号是否存在 |
| `add-jvm-arg <参数>` | 添加一条 JVM 参数 |
| `remove-jvm-arg <参数>` | 删除第一条匹配的 JVM 参数 |
| `add-game-arg <参数>` | 添加一条游戏参数 |
| `remove-game-arg <参数>` | 删除第一条匹配的游戏参数 |
| `reset` | 将实例配置重置为默认值（包括解绑默认账号） |

示例：

```bash
./mtl config show --dir ./instances/demo
./mtl config set-memory manual --value 6G --dir ./instances/demo
./mtl config set-memory auto --dir ./instances/demo
./mtl config set-highperf-gpu true --dir ./instances/demo
./mtl config set-default-account --dir ./instances/demo Steve
./mtl config add-jvm-arg --dir ./instances/demo "-XX:+UseG1GC"
./mtl config remove-jvm-arg --dir ./instances/demo "-XX:+UseG1GC"
./mtl config add-game-arg --dir ./instances/demo "--width"
./mtl config reset --dir ./instances/demo
```

解绑默认账号（传空字符串）：

```bash
./mtl config set-default-account --dir ./instances/demo ""
```

说明：

- `--dir=...`、`--value=...` 写法同样可用。
- 以 `-` 开头的 JVM 参数可直接作为位置参数，不需要额外的 `--`。
- 配置修改在下次 `mtl run` 时生效。

### mtl version

```bash
./mtl version
```

当前开发版输出 `mtl dev`。

## 📁 目录结构说明

### 实例目录

```
instances/
└── demo/
    ├── .mtl/
    │   └── config.json          # MTL 实例配置
    ├── crash_reports/           # MTL 归档的崩溃日志（下划线）
    ├── crash-reports/           # Minecraft 原版生成的崩溃日志（连字符，崩溃后出现）
    ├── mods/                    # 整合包 mod
    ├── config/                  # 整合包自带配置
    ├── libraries/               # 游戏库与 Fabric loader 库
    ├── versions/                # 原版版本 json/jar 与 Fabric profile
    ├── logs/
    ├── saves/
    └── ...                      # resourcepacks、data、downloads 等由整合包决定
```

MTL 直接把实例根目录作为游戏目录（`--gameDir`），因此 `mods/`、`config/`、`saves/` 都在实例根目录下，不再额外套一层 `.minecraft/`。正常实例中通常不存在 `.minecraft/` 目录。

注意 `crash_reports/`（下划线）是 MTL 的归档目录，`crash-reports/`（连字符）是 Minecraft 原版生成的原始崩溃日志目录。

### 全局缓存目录

Windows：

```
%APPDATA%\mtl\cache\
├── runtime\<component>\      # Mojang 官方 Java 运行时
├── assets\                   # 原版资源索引与对象
└── accounts\offline.json     # 离线账号
```

Linux / macOS：`~/.cache/mtl/` 下为相同结构。

- `runtime/` 与 `assets/` 由所有实例共享，可安全复用。
- 账号数据单独放在 `accounts/`，与实例、runtime、assets 隔离。
- 删除 `runtime/` 或 `assets/` 只会导致下次重新下载，不会损坏已有实例。

## 📝 实例配置 config.json 字段说明

默认配置示例：

```json
{
  "memory_mode": "auto",
  "manual_memory": "",
  "jvm_args": [],
  "game_args": [],
  "prefer_high_perf_gpu": false,
  "default_account": "",
  "java": ""
}
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `memory_mode` | string | `auto` 或 `manual`。缺省或空值按 `auto` 处理 |
| `manual_memory` | string | 手动模式下的最大堆，如 `4G`、`512M`；仅 `manual` 模式生效 |
| `jvm_args` | string[] | 追加的 JVM 参数，排在 `-Xms / -Xmx` 之后 |
| `game_args` | string[] | 追加的游戏参数，只排在主类之后 |
| `prefer_high_perf_gpu` | bool | 是否启用高性能显卡调度，仅 Windows 生效，默认 `false` |
| `default_account` | string | 实例默认离线账号名；空字符串表示未绑定 |
| `java` | string | 上次成功启动使用的 Java 可执行文件绝对路径；空表示自动匹配。路径失效时自动重新匹配 |

补充说明：

- 自动内存模式：物理内存不超过 16G 时，取物理内存的 40% 且最多 6G；超过 16G 时最多 8G。`-Xms` 默认为 `-Xmx` 的 1/4，且不低于 256M。
- 手动内存会被校验：不得低于 256M，不得超过物理内存的 90%。
- 配置文件损坏或字段非法时，MTL 会告警并回退到默认配置，不会阻止启动。
- 可直接编辑 JSON，也可使用 `mtl config` 子命令修改。

## 🧪 崩溃诊断说明

`mtl run` 在游戏进程退出后执行一次崩溃归档：

1. 启动前记录 `crash-reports/` 与 `.minecraft/crash-reports/` 中已有崩溃日志的状态。
2. 游戏退出后，只处理本次新增或内容发生变化的 `crash-*.txt`。
3. 将日志复制到实例的 `crash_reports/`，文件名格式为 `crash-<UTC时间戳>-<原文件名>.txt`。
4. 在归档文件顶部追加诊断摘要，同时在终端输出 `[Crash]` 提示。

诊断基于**离线关键词扫描**，当前覆盖：

- Mixin 注入冲突（`mixin apply failed`、`mixin injection`）
- 重复 Mod（`mod id already registered`、`duplicate mod id`）
- 缺少前置依赖（`requires mod`、`dependency not found`、`missing dependency`）
- 类缺失（`NoClassDefFoundError` 附近出现 mod / fabric / mixin 等标识）
- Minecraft 版本不兼容（`wrong minecraft version`、`minecraft version mismatch`）
- 渲染类 Mod 冲突（oculus、rubidium、embeddium、magnesium、optifine、optifabric、canvas 等）
- 全部未命中时输出兜底提示，建议直接查看原始日志

适用场景与局限：

- 适合快速定位 Fabric 整合包最常见的启动崩溃。
- 关键词匹配可能误报（例如日志中的 Mod 列表恰好包含相关词），也可能漏报。
- 深度混淆堆栈、显卡/驱动问题、内存不足、Java 版本问题不在诊断范围内。
- 归档或诊断失败只告警，不会阻断启动流程。

## 📄 .gitignore 说明

仓库的 `.gitignore` 主要忽略：

- 编译产物：`bin/`、`*.exe`、`*.dll`、`*.so`、`*.dylib`、`*.test`
- 本地实例：`instances/`（mod、存档、游戏文件都在这里，不要提交）
- 本地测试 mrpack 目录：`mtl-test/`
- 本机私有配置：`.claude/settings.local.json`、`.claudeignore`
- 覆盖率与临时文件：`coverage.out`、`*.log`
- IDE 与系统垃圾：`.idea/`、`.vscode/`、`.DS_Store`、`Thumbs.db`
- 环境文件：`.env`、`*.local.json`

## 🤝 开发说明

模块名为 `mtl`，入口在 `cmd/mtl`。主要包职责：

- `internal/mrpack`：mrpack 解析、路径安全（`SafeJoin` / `ValidateRelPath`）、overrides 解压
- `internal/downloader`：并发下载、哈希校验、失败重试
- `internal/fabric`：Fabric loader 安装与版本文件准备
- `internal/runtime`：Mojang 官方 Java 运行时下载、校验、缓存
- `internal/assets`：原版资源索引与对象缓存
- `internal/launch`：启动参数组装、进程拉起、Windows 显卡调度
- `internal/config`：实例配置读写与参数优先级合并
- `internal/account`：离线账号、用户名校验、离线 UUID
- `internal/memory`：内存值解析、校验与自动推荐
- `internal/crash`：崩溃日志归档与离线诊断

开发约定：

- 优先增量修改，不重写已有的 mrpack / downloader / fabric / launch 核心逻辑。
- 新增功能尽量放到独立包中，保持包职责单一。
- 所有路径都经过 `SafeJoin`，禁止路径穿越。
- 不执行外部未知脚本，不接入微软登录接口，优先离线实现。
- 写操作尽量原子化、幂等，重复执行不损坏实例或缓存。
- 新增逻辑补充单元测试。

提交前执行：

```bash
go vet ./...
go test ./...
```

## 📌 后续规划

- 支持 Forge / NeoForge mrpack
- mrpack 增量更新，并清理新版本中已移除的文件
- 皮肤 / 披风（离线场景下的有限支持）
- Mod 管理与冲突可视化
- 更多 PCL2 启动器细节对齐（下载源、镜像、版本隔离策略等）
