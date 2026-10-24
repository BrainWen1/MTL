# MTL (My Tiny Launcher)

轻量级命令行 Minecraft 启动器，原生支持 mrpack 整合包，主打 **Fabric / Forge + 离线模式**。

MTL 是自用项目，不是成熟的发行版启动器。功能按实际需要逐步迭代，优先离线、轻量、少联网。

## ✨ 已实现功能

- **mrpack 整合包安装**：解析 `modrinth.index.json`，下载客户端文件、解压 overrides，并自动安装对应版本的 Fabric 或 Forge loader（Forge 需 Minecraft 1.20.1+）。
- **mrpack 增量更新**：通过 `.mtl/pack-lock.json` 记录托管文件与 sha256；重复安装时只下载变更文件，并清理新版整合包不再包含的旧托管文件。`saves/`、用户手动新增或修改的文件不会被清理，可用 `--no-prune` 完全关闭清理。
- **纯净模板安装**：`mtl install --dir <目录> --mc <版本>` 可不依赖 mrpack 直接安装原版 / Fabric / Forge；复用 Mojang 清单、全局 assets 缓存与官方加载器流程，不生成 pack-lock。
- **加载器抽象**：`internal/loader` 统一 Fabric / Forge 的安装与启动接口；安装时把 `loader_type`、`loader_version` 写入实例配置，旧实例按 profile id 自动识别。
- **Forge 安装**：下载并校验官方 installer 到全局缓存，准备原版文件与 `launcher_profiles.json`，以非 GUI 方式执行 installer；已安装的 profile 和 installer 缓存会复用。
- **哈希校验与自动跳过**：下载前校验已有文件的 sha1 / sha512，哈希一致的直接跳过；重复执行安装不会重复下载。
- **断点续传与原子写入**：所有下载（mod、libraries、assets、Java runtime、mrpack）先写 `*.tmp`，支持 HTTP Range 续传；校验通过后才改名，中断（含 `Ctrl+C`）后重新执行会自动接着下载。
- **镜像源与日志模式**：可配置 Mojang / mod / Fabric / Forge 镜像，默认优先镜像、失败自动回退官方源；下载日志默认汇总模式（`summary`），`--debug` 或 `--log-mode verbose` 可查看逐文件详情。
- **实例隔离**：每个整合包一个独立实例目录，mod、配置、存档、库文件互不影响。
- **Java 运行时自动准备**：按版本要求从 Mojang 官方运行时清单下载并校验，缓存复用；也可用 `--java` 指定本机 Java。
- **原版资源缓存**：assets 索引与资源对象下载到全局缓存，多个实例共享。
- **内存配置**：支持自动推荐或手动指定 `-Xms / -Xmx`，并校验不低于 256M、不超过物理内存的 90%。
- **JVM / 游戏参数配置**：可写入实例配置，也可在命令行单次追加；JVM 参数与游戏参数严格分离。
- **Windows 高性能显卡调度**：为 Java 可执行文件写入 `HKCU` 显卡首选项，并追加 D3D 参数；设置失败只告警，不影响启动。
- **离线账号**：全局保存离线账号（用户名 + 离线 UUID），实例可绑定默认账号，`--account` 可临时切换；同一台机器可用多个账号进行局域网联机。
- **崩溃日志归档与诊断**：每次启动后自动归档新增崩溃日志，并用离线关键词规则给出 Mod 冲突提示。
- **整合包查看**：`mtl inspect` 可在安装前查看整合包信息，按需解压 overrides。
- **实例列表与健康检查**：`mtl list` 只读扫描实例根目录，用表格展示加载器、默认账号和 Java 状态；`mtl info` 展示完整配置与健康检查报告。
- **本地 Mod 管理**：`mtl mod list|enable|disable|rm` 离线读取 Fabric / Forge 元数据（含嵌套 jar 子模块），展示名称、版本、id、加载器与依赖；禁用只把 jar 移动到 `mods/disabled/`，不修改文件内容，可随时恢复。

## ⚠️ 当前已知限制

- Forge 仅支持 **Minecraft 1.20.1 及以上**；NeoForge、Quilt 尚不支持（Quilt 依赖会被识别，但安装时会明确报错）。
- Forge 安装依赖 Forge 官方 Maven 与 installer，首次安装耗时较长，网络受限时可能失败。
- 仅有离线账号体系，不接入微软正版登录；启动参数中的 access token 固定为 `0`，用户类型为 `legacy`。
- 高性能显卡调度仅 Windows 生效，其他平台静默跳过。
- 崩溃诊断是**静态关键词匹配**，只能覆盖常见模式；复杂崩溃、混淆堆栈或非 Mod 问题仍需人工查看原始日志。
- 增量清理只识别 mrpack 安装写入 `.mtl/pack-lock.json` 的托管文件；旧实例首次重新安装不会清理历史文件，只会从本次安装开始建立锁定清单。
- 纯净模板实例不生成 pack-lock，也不参与整合包清理；其中的 mod、配置与存档完全由用户手动管理。
- 清理前会校验文件哈希，用户修改过的旧托管文件会保留；`saves/` 与 `.mtl/` 永远不参与清理。
- 没有图形界面、皮肤/披风、在线 Mod 市场、自动更新等启动器周边功能。
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

安装会下载整合包声明的客户端文件、解压 overrides，并安装对应的 Fabric / Forge loader。耗时取决于网络，已存在且哈希正确的文件会自动跳过。

如果只想安装纯净实例，可改用模板模式：

```bash
./mtl install --dir ./instances/vanilla --mc 1.20.1
./mtl install --dir ./instances/fabric --mc 1.20.1 --loader fabric --loader-version 0.15.11
```

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

安装实例，支持两种互斥模式：

- **mrpack 整合包模式**：传入一个 `.mrpack` 文件，安装整合包及其声明的加载器。
- **纯净模板模式**：不依赖整合包，用 `--mc` 指定版本，可选安装 Fabric / Forge。

```bash
# 模式一：mrpack 整合包
mtl install --dir <实例目录> [--concurrency N] [--skip-loader] [--no-prune] \
            [--mirror URL] [--mod-mirror URL] [--retries N] [--timeout 30s] \
            [--log-mode summary|verbose] [--debug] <pack.mrpack>

# 模式二：纯净模板
mtl install --dir <实例目录> --mc <版本> [--loader fabric|forge] [--loader-version <版本>] \
            [--mirror URL] [--mod-mirror URL] [--retries N] [--timeout 30s] \
            [--log-mode summary|verbose] [--debug]
```

| 参数 | 适用模式 | 必填 | 说明 |
| --- | --- | --- | --- |
| `--dir <目录>` | 两者 | 是 | 实例目录，不存在时自动创建 |
| `--concurrency N` | 两者 | 否 | 并发下载数，缺省为 4 |
| `<pack.mrpack>` | mrpack | 是 | mrpack 文件路径 |
| `--mc <版本>` | 模板 | 是 | Minecraft 版本，例如 `1.20.1` |
| `--loader <类型>` | 模板 | 否 | `fabric` 或 `forge`；省略表示纯原版 |
| `--loader-version <版本>` | 模板 | 条件 | 指定 `--loader` 时必填 |
| `--skip-loader` | mrpack | 否 | 只下载整合包文件，不安装 Fabric / Forge loader |
| `--no-prune` | mrpack | 否 | 增量更新时只更新文件，不删除旧版整合包遗留的托管文件 |
| `--mirror <URL>` | 两者 | 否 | 临时覆盖 Mojang 资源镜像（assets / libraries / 版本元信息），失败自动回退官方源 |
| `--mod-mirror <URL>` | 两者 | 否 | 临时覆盖 mod 下载源镜像前缀 |
| `--retries N` | 两者 | 否 | 失败后的额外重试次数；`0` 使用配置值，`-1` 表示不重试 |
| `--timeout <时长>` | 两者 | 否 | 单文件下载超时，例如 `30s`、`2m`；`0` 表示使用配置值或不限制 |
| `--log-mode <模式>` | 两者 | 否 | `summary`（默认，批次进度 + 失败清单）或 `verbose`（逐文件） |
| `--debug` | 两者 | 否 | 输出调试信息，并自动切换为 `verbose` 下载日志 |

示例：

```bash
# mrpack 整合包
./mtl install --dir ./instances/demo ./test-pack.mrpack

# 纯原版 1.20.1
./mtl install --dir ./instances/vanilla --mc 1.20.1

# Fabric 1.20.1
./mtl install --dir ./instances/fabric --mc 1.20.1 --loader fabric --loader-version 0.15.11

# Forge 1.20.1（需 Minecraft 1.20.1+）
./mtl install --dir ./instances/forge --mc 1.20.1 --loader forge --loader-version 47.4.3
```

模式冲突会直接报错：mrpack 文件不能与 `--mc` / `--loader` / `--loader-version` 同时使用；`--skip-loader`、`--no-prune` 只适用于 mrpack 模式；`--loader-version` 必须与 `--loader` 一起使用。

#### mrpack 整合包模式

- Fabric 与 Forge 的选择完全由 mrpack 的 `dependencies` 决定。
- Forge 仅支持 Minecraft 1.20.1+；Forge 版本可写完整形式 `1.20.1-47.4.3`，也可只写 `47.4.3`。
- 重复安装会复用已校验的文件、已安装的 Forge profile 与 installer 缓存，不会重复执行 installer。

#### 纯净模板模式

- 从 Mojang 官方版本清单校验版本号，下载版本 json、client jar 与 libraries；assets 写入全局缓存，多个实例共享。
- 纯原版会生成一个 `inheritsFrom` 原版的薄 profile（`versions/<版本>-mtl-vanilla/`），现有 `mtl run` 无需额外参数即可启动。
- Fabric 通过 Fabric meta 安装；Forge 复用官方 installer 流程，要求 Minecraft 1.20.1+。
- 写入 `.mtl/config.json` 的 `loader_type`、`loader_version` 与 `install_mode=template`，`mtl info` 会标记为「手动纯净模板」。
- **不会写入 `.mtl/pack-lock.json`**，因此手动放入的 mod、配置和存档永远不会触发整合包清理逻辑。
- 已存在且哈希正确的文件会自动跳过，可安全重复执行；网络失败或版本不存在会输出明确的中文错误。

增量更新说明（仅 mrpack 模式）：

- 安装完成后写入 `.mtl/pack-lock.json`，记录整合包名称/版本、来源路径，以及所有由 mrpack 自动下载或解压的托管文件（相对路径 + `sha256-` 哈希）。
- 再次安装同一个整合包时，会对比新旧锁定清单并输出“新增 / 更新 / 待删除”摘要；新版不再声明的旧托管文件会先打印列表再删除。
- 只有「旧锁定清单记录、新版 mrpack 不再声明、且当前哈希仍与旧锁定一致」的文件才会删除。`saves/`、`.mtl/` 永远不清理；用户手动新增的文件不在清单中，用户修改过的旧托管文件会保留。
- 第一次安装、旧实例没有 `pack-lock.json`、或整合包标识（`name`）发生变化时，只安装并生成新清单，不做清理，避免跨整合包误删。
- `--no-prune` 用于兜底：本次只更新文件、保留全部旧托管文件。被保留的文件不会继续写入新清单，之后的普通安装也不会再自动清理它们。

### mtl run

启动指定实例。

```bash
mtl run --dir <实例目录> [--account <账号>] [--username <名字>] [--memory <内存>] \
        [--memory-auto] [--highperf-gpu|--no-highperf-gpu] [--java <路径>] \
        [--jvm-arg <参数>]... [--game-arg <参数>]... \
        [--mirror URL] [--mod-mirror URL] [--retries N] [--timeout 30s] \
        [--log-mode summary|verbose] [--debug]
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
| `--mirror <URL>` | 本次启动临时覆盖 Mojang 资源镜像，失败自动回退官方源 |
| `--mod-mirror <URL>` | 本次启动临时覆盖 mod 下载源镜像前缀 |
| `--retries N` | 下载失败后的额外重试次数；`0` 使用配置值，`-1` 表示不重试 |
| `--timeout <时长>` | 单文件下载超时，例如 `30s`、`2m` |
| `--log-mode <模式>` | 下载日志粒度：`summary`（默认）或 `verbose` |
| `--debug` | 打印配置来源、完整启动命令和调试信息，并切换为 `verbose` 下载日志 |

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
- 游戏成功运行结束后，本次自动匹配或实例配置选中的 Java 绝对路径会回填到实例配置（与已有值相同则跳过）；启动失败或中途崩溃不会修改配置。
- `--java` 只对本次启动临时生效，不会写入实例配置。
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

### mtl list

只读扫描实例根目录（默认 `./instances`），输出每个实例的加载器、默认账号和 Java 路径状态。

```bash
mtl list [--dir <实例根目录>]
```

| 参数 | 说明 |
| --- | --- |
| `--dir <目录>` | 实例根目录，缺省为当前目录下的 `instances` |

示例：

```bash
./mtl list
./mtl list --dir ./instances
```

输出表格包含：实例名、加载器类型、加载器版本、默认账号、Java 状态。`loader_type` / `loader_version` 为空时显示 `-`；Java 路径异常会以 `!!` 标记。

说明：

- 只扫描根目录下的一级子目录，不递归。
- 缺少 `.mtl/config.json` 或配置损坏的目录会输出警告并跳过，不影响其他实例。

### mtl info

查看单个实例的完整配置和健康检查结果。

```bash
mtl info <实例名> [--dir <实例根目录>]
```

| 参数 | 说明 |
| --- | --- |
| `<实例名>` | 必填，实例根目录下的目录名 |
| `--dir <目录>` | 实例根目录，缺省为当前目录下的 `instances` |

示例：

```bash
./mtl info demo
./mtl info --dir ./instances demo
./mtl info demo --dir ./instances
```

健康检查当前覆盖：

- Java 路径为空 → 自动匹配，视为正常；路径不存在或不可执行 → 标记异常。
- `loader_type` / `loader_version` 为空 → 警告并显示 `-`，不影响命令成功。
- 未绑定默认账号 → 警告。
- 加载器不是 `vanilla` / `fabric` / `forge` → 警告。

说明：

- 只读操作，不修改任何配置文件，也不启动游戏。
- Java 检查只校验文件是否存在、是否为可执行文件，不会执行 `java -version`。
- mrpack 实例的元信息会展示整合包版本、`.mtl/pack-lock.json` 是否存在及托管文件数量、整合包来源；锁定清单损坏时显示读取失败，不影响其他检查。
- 纯净模板实例标记为「手动纯净模板」，不展示 pack-lock 相关信息。

### mtl mod

管理单个实例的本地 mod：查看元数据、启用/禁用与删除。模板实例（`install_mode=template`）与 mrpack 整合包实例均可使用。

```bash
mtl mod list --dir <实例目录> [--debug]
mtl mod enable <mod-id|文件名> --dir <实例目录>
mtl mod disable <mod-id|文件名> --dir <实例目录>
mtl mod rm <mod-id|文件名> --dir <实例目录>
```

| 子命令 | 说明 |
| --- | --- |
| `list` | 列出 `mods/` 与 `mods/disabled/` 下的所有 mod，区分启用/禁用 |
| `enable` | 把禁用目录中的 mod 移回 `mods/` |
| `disable` | 把 mod 移动到 `mods/disabled/`，不修改 jar 内容 |
| `rm` | 删除 mod 文件；启用或禁用状态均可删除 |

| 参数 | 说明 |
| --- | --- |
| `--dir <目录>` | 必填，实例目录 |
| `--debug` | 在表格之后输出完整元数据（版本约束、可选依赖、嵌套模块等） |
| `<mod-id\|文件名>` | mod id、`xxx.jar` 或不带后缀的文件名；均忽略大小写 |

示例：

```bash
./mtl mod list --dir ./instances/demo
./mtl mod list --dir ./instances/demo --debug
./mtl mod disable sodium --dir ./instances/demo
./mtl mod enable sodium-fabric-0.5.0.jar --dir ./instances/demo
./mtl mod rm "some-unknown-mod.jar" --dir ./instances/demo
```

输出与校验说明：

- 默认输出简洁表格：状态、MOD ID、名称、版本、加载器、依赖、文件；`--debug` 追加每个 mod 的完整元数据。
- 元数据读取优先级：`fabric.mod.json` > `META-INF/neoforge.mods.toml` > `META-INF/mods.toml`。缺失或解析失败时显示「未知mod」，不中断列表。
- 会读取 `META-INF/jars/` 中的嵌套模块（例如 Fabric API 的子模块），避免把包内模块误报为缺失依赖。
- 以下情况在 `list` 末尾输出警告，但不阻断命令：重复 mod id、加载器与实例不匹配、启用中的 mod 缺少必需前置、前置已安装但被禁用。
- 校验完全离线：只读取本地 jar，不联网、不校验签名，也不判断 Mod 版本约束是否真正满足。
- 禁用通过文件移动实现，jar 内容保持不变；`mods/disabled/` 属于用户手动状态，mrpack 增量清理永远不会删除或移动其中的文件。
- `enable` / `disable` 可安全重复执行；目标位置已存在同名文件时会报错，不会覆盖已有 mod。

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
mtl config set-java <实例目录> [java路径]（省略路径表示清空）
mtl config set-highperf-gpu <true|false> --dir <实例目录>
mtl config set-default-account --dir <实例目录> <账号名>
mtl config add-jvm-arg --dir <实例目录> <参数>
mtl config remove-jvm-arg --dir <实例目录> <参数>
mtl config add-game-arg --dir <实例目录> <参数>
mtl config remove-game-arg --dir <实例目录> <参数>
mtl config reset --dir <实例目录>
mtl config global（显示全局配置）
mtl config set-mirror <类型> <镜像地址>（传空字符串恢复官方源）
```

| 子命令 | 说明 |
| --- | --- |
| `show` | 打印当前实例配置 |
| `set-memory auto` | 使用自动内存模式 |
| `set-memory manual --value 4G` | 使用手动内存，`--value` 必填 |
| `set-java <java路径>` | 校验并写入实例 Java 路径，不启动游戏；省略路径表示清空并恢复自动匹配 |
| `set-highperf-gpu true` | 开启实例级高性能显卡调度（仅 Windows 生效） |
| `set-highperf-gpu false` | 关闭实例级高性能显卡调度 |
| `set-default-account <账号名>` | 绑定实例默认离线账号；只保存名字，不校验账号是否存在 |
| `add-jvm-arg <参数>` | 添加一条 JVM 参数 |
| `remove-jvm-arg <参数>` | 删除第一条匹配的 JVM 参数 |
| `add-game-arg <参数>` | 添加一条游戏参数 |
| `remove-game-arg <参数>` | 删除第一条匹配的游戏参数 |
| `reset` | 将实例配置重置为默认值（包括解绑默认账号） |
| `global` | 显示全局配置路径与内容（镜像源、下载日志模式等） |
| `set-mirror <类型> <地址>` | 修改全局镜像；类型：`mojang`、`assets`、`libraries`、`version-meta`、`mod`、`fabric`、`forge` |

示例：

```bash
./mtl config show --dir ./instances/demo
./mtl config set-memory manual --value 6G --dir ./instances/demo
./mtl config set-memory auto --dir ./instances/demo
./mtl config set-java ./instances/demo "C:\Java\jdk-21\bin\java.exe"
./mtl config set-java ./instances/demo          # 清空，恢复自动匹配
./mtl config set-highperf-gpu true --dir ./instances/demo
./mtl config set-default-account --dir ./instances/demo Steve
./mtl config add-jvm-arg --dir ./instances/demo "-XX:+UseG1GC"
./mtl config remove-jvm-arg --dir ./instances/demo "-XX:+UseG1GC"
./mtl config add-game-arg --dir ./instances/demo "--width"
./mtl config reset --dir ./instances/demo
./mtl config global
./mtl config set-mirror mojang https://mirror.example/mc
./mtl config set-mirror mojang ""     # 恢复官方源
```

#### 全局配置与下载层

全局配置对所有实例生效，与实例配置分离：

- Windows：`%APPDATA%/mtl/config.json`
- Linux / macOS：`~/.config/mtl/config.json`
- 可用环境变量 `MTL_CONFIG_PATH` 指定其他路径

```json
{
  "mirrors": {
    "mojang": "https://mirror.example/mc",
    "assets": "",
    "libraries": "",
    "version_meta": "",
    "mod": "",
    "fabric": "",
    "forge": ""
  },
  "download": {
    "log_mode": "summary",
    "retries": 0,
    "timeout": "30s",
    "concurrency": 4
  }
}
```

优先级：**命令行参数 > 环境变量 > 全局配置文件 > 默认官方源**。镜像只替换域名（以及镜像自带的前缀路径），资源路径保持不变；镜像请求失败后自动回退原始官方地址。

环境变量：`MTL_MOJANG_MIRROR`、`MTL_ASSETS_MIRROR`、`MTL_LIBRARIES_MIRROR`、`MTL_VERSION_META_MIRROR`、`MTL_MOD_MIRROR`、`MTL_FABRIC_MIRROR`、`MTL_FORGE_MIRROR`、`MTL_DOWNLOAD_LOG`、`MTL_DOWNLOAD_RETRIES`、`MTL_DOWNLOAD_TIMEOUT`、`MTL_DOWNLOAD_CONCURRENCY`。

下载失败会继续处理其他文件，最后按「网络失败 / 哈希校验失败 / 不支持断点续传 / 本地文件错误」汇总；中断后重新执行同一命令会从 `*.tmp` 继续下载。

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
    │   ├── config.json          # MTL 实例配置
    │   └── pack-lock.json       # 整合包托管文件锁定清单（仅 mrpack 实例，纯净模板没有）
    ├── crash_reports/           # MTL 归档的崩溃日志（下划线）
    ├── crash-reports/           # Minecraft 原版生成的崩溃日志（连字符，崩溃后出现）
    ├── mods/                    # 整合包 mod
    │   └── disabled/            # mtl mod disable 移动到此，jar 内容不变
    ├── config/                  # 整合包自带配置
    ├── libraries/               # 游戏库与 Fabric / Forge loader 库
    ├── versions/                # 原版版本 json/jar 与加载器 profile
    ├── launcher_profiles.json   # Forge installer 前置文件；纯净模板也会生成最小骨架
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
  "loader_type": "",
  "loader_version": "",
  "install_mode": "",
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
| `loader_type` | string | 安装时使用的加载器：`vanilla` / `fabric` / `forge`；由 `mtl install` 写入，旧实例为空时按 profile id 推断 |
| `loader_version` | string | 加载器版本；纯原版记录 Minecraft 版本，Fabric/Forge 记录加载器版本；由 `mtl install` 写入 |
| `install_mode` | string | `template` 表示不依赖 mrpack 的纯净模板实例；留空表示 mrpack 实例。由 `mtl install --mc` 写入 |
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

- 适合快速定位 Fabric / Forge 整合包最常见的启动崩溃。
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

- `internal/mrpack`：mrpack 解析、路径安全（`SafeJoin` / `ValidateRelPath`）、overrides 解压、pack-lock 锁定清单与增量清理
- `internal/downloader`：并发下载、哈希校验、失败重试
- `internal/loader`：加载器统一抽象与按类型构造（Vanilla / Fabric / Forge，NeoForge 预留）
- `internal/vanilla`：纯原版模板安装、薄 profile 与 assetIndex 元数据解析
- `internal/template`：不依赖 mrpack 的模板安装编排（纯原版 / Fabric / Forge）
- `internal/fabric`：Fabric loader 安装、原版版本文件准备
- `internal/forge`：Forge installer 下载与调用、profile/libraries 准备（Minecraft 1.20.1+）
- `internal/runtime`：Mojang 官方 Java 运行时下载、校验、缓存
- `internal/assets`：原版资源索引与对象缓存
- `internal/launch`：版本 json 解析、启动参数组装（Fabric / Forge）、进程拉起、Windows 显卡调度
- `internal/config`：实例配置读写与参数优先级合并
- `internal/instance`：实例根目录只读扫描、Java 路径状态与健康检查
- `internal/account`：离线账号、用户名校验、离线 UUID
- `internal/memory`：内存值解析、校验与自动推荐
- `internal/crash`：崩溃日志归档与离线诊断

开发约定：

- 优先增量修改，不重写已有的 mrpack / downloader / fabric / forge / launch 核心逻辑。
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

## 📌 后续规划（轻量对标 PCL2）

排序原则：先补“装得上、跑得稳”的地基，再做日常管理，最后才是体验细节。保持 CLI、离线优先，不接入微软登录。

### P0 — 下一阶段优先完成

1. ✅ **mrpack 增量更新与残留清理（已实现）**
   - 安装后写入 `.mtl/pack-lock.json`：包标识、版本、来源、托管文件路径与 sha256。
   - 再次 `install` 时对比新旧清单：只下载变更文件，删除新包中已移除的托管文件；清理前输出新增/更新/待删除摘要。
   - 保护用户数据：`saves/`、`.mtl/`、手动新增的文件不参与清理；用户修改过的旧托管文件经哈希校验后保留。
   - `--no-prune` 可完全关闭清理，只更新文件。
   - 验收：同一实例连续安装 v1 → v2 后 `mods/` 无 v1 残留，存档与手动文件保留。

2. ✅ **不依赖整合包的原版 / 加载器安装（已实现）**
   - 新增 `mtl install --dir <目录> --mc <版本> [--loader fabric|forge] [--loader-version <版本>]`。
   - 复用现有 `fabric` / `forge` / `assets` 包；`--loader` 省略时只装原版。
   - 写入 `install_mode=template`，不生成 pack-lock；`mtl info` 标记为手动纯净模板。
   - 验收：空目录安装 1.20.1 原版、Fabric、Forge 后均可直接 `mtl run`。

3. ✅ **下载断点续传与镜像源**
   - 单文件 `Range` 续传（`.part` 临时文件），重试不再从头下载。
   - 全局配置 Mojang / Fabric / Forge Maven / Modrinth 下载源，支持环境变量或全局配置文件覆盖；镜像失败自动回退官方。
   - 验收：下载中断后重新执行可从断点继续；镜像不可用时仍能完成安装。

### P1 — 日常管理能力

4. ✅ **本地 Mod 管理（已实现）**
   - `mtl mod list|enable|disable|rm`：离线读取 `fabric.mod.json` / `META-INF/mods.toml`（含 `META-INF/jars/` 嵌套模块），展示名称、版本、id、加载器与依赖。
   - 离线检测重复 Mod、加载器不匹配、缺失前置；禁用通过移动到 `mods/disabled/` 实现，不修改 jar，可随时恢复。
   - mrpack 增量清理会跳过 `mods/disabled/`，不会删除用户手动禁用的 mod。
5. **Java 运行时管理**
   - `mtl java list|use|prune`：查看缓存运行时、手动登记本机 Java、清理未使用版本。
   - 与现有实例 `java` 字段、`--java` 优先级保持一致。
6. **崩溃诊断与启动日志增强**
   - 每次启动落盘独立日志，保留最近 N 次；崩溃摘要附带 Mod 列表快照。
   - 补充 Java 版本不匹配、内存不足（OOM）、显卡驱动等离线规则。
7. **实例备份与恢复**
   - `mtl backup --dir <目录> [--include mods|config|saves|all]`，输出可恢复的压缩包。
   - 支持启动前自动备份存档，避免整合包更新或崩溃损坏世界。

### P2 — 体验对齐（按需）

8. **服务器与局域网联机辅助**：`mtl server add|list|rm`，启动时 `--server` 快速加入；补充局域网端口与 `online-mode=false` 检查提示。
9. **资源包 / 光影包管理**：列出、启用、禁用、删除，复用 Mod 管理的文件移动策略。
10. **authlib-injector 第三方登录（可选）**：仅支持用户自建 Yggdrasil，仍然不接入微软账号；默认关闭。
11. **发布与自更新**：版本号注入、GoReleaser 式多平台构建、可校验的二进制自更新。
12. **轻量 TUI**：在现有命令上提供实例选择、安装进度、崩溃摘要的单屏界面，不引入 GUI 依赖。

### 明确暂不做

- 微软正版登录、在线皮肤 / 披风服务。
- 图形界面与在线 Mod 市场（联网接口、审核与版权维护成本过高）。
- 重写现有 `mrpack` / `runtime` / `assets` / `fabric` / `forge` / `launch` 核心逻辑；新能力优先独立成包。
