# MTL - Minecraft启动器 项目规约
项目：mtl，Go语言开发，CLI启动器，对标PCL2基础能力，当前阶段：离线账号模块
约束：
1. 安全：全部路径使用SafeJoin，禁止路径穿越；不执行外部未知脚本；不请求微软登录接口。
2. Token节约：优先增量修改，不重写已有runtime/assets/mrpack/fabric/launch核心逻辑；只新增必要代码。
3. 测试优先：增加单元测试；go vet ./...、go test ./...必须全部pass。
4. 缓存规范：缓存统一放在%APPDATA%/mtl/cache（Windows）、~/.cache/mtl（Linux/macOS）；账号数据单独存放，和runtime、assets隔离。
5. 日志规范：统一前缀 [Account]，不疯狂刷屏，关键步骤输出。
6. 幂等：重复执行不会损坏实例、缓存文件。

已有能力（不要改动）
- mrpack整合包解析、安装
- assets资源下载、校验、缓存
- Mojang官方Java runtime自动下载/校验/缓存，--java参数优先
- Fabric加载器安装，游戏进程拉起

当前开发目标：离线账号模块，服务局域网联机
