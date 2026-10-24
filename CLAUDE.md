# MTL - Minecraft启动器 项目规约
项目：mtl，Go语言开发，CLI启动器，对标PCL2基础能力，面向Minecraft Fabric/vanilla，支持mrpack整合包
约束：
1. 安全：全部路径使用SafeJoin，禁止路径穿越；不执行外部未知脚本；不请求微软软登录接口，仅离线账号。
2. Token节约：优先增量修改，不重写已有runtime/assets/mrpack/fabric/launch核心逻辑；只新增必要代码。
3. 测试优先：增加单元测试；go vet ./...、go test ./...必须全部pass。
4. 缓存规范：缓存统一放在%APPDATA%/mtl/cache（Windows）、~/.cache/mtl（Linux/macOS）；账号数据、实例数据单独存放，和runtime、assets隔离。
5. 日志规范：统一分级日志，带时间戳+模块标识；不疯狂刷屏，默认INFO，DEBUG按需开启；每次run自动在实例logs目录生成独立日志文件。
6. 幂等：重复执行不会损坏实例、缓存文件；下载、解压、配置写入全部具备幂等能力。

已有能力（不要改动）
- mrpack整合包解析、安装
- assets资源下载、校验、缓存
- Mojang官方Java runtime自动下载/校验/缓存；--java参数优先，支持全局默认java管理
- Fabric加载器安装，游戏进程拉起
- 离线账号创建、切换，独立UUID
- run子命令：支持--memory自动内存、自定义JVM参数、游戏参数
- 实例配置持久化，单实例独立配置，参数优先级：cli参数 > 实例配置 > 全局配置 > 自动匹配
- mtl mod：实例mod管理
- mtl java：全局runtime list / use / prune 管理

当前开发目标：日志增强、mtl diagnose崩溃诊断子命令，识别OOM、mod冲突、java版本异常等崩溃，输出诊断建议

