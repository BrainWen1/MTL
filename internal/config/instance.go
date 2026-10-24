package config

// Instance 描述一个待安装或待启动的实例，是加载器接口的统一输入。
// 它只承载加载器所需的最小信息，不参与 config.json 的序列化。
type Instance struct {
	// Dir 是实例根目录。
	Dir string
	// MinecraftVersion 是原版版本号，例如 1.20.1。
	MinecraftVersion string
	// LoaderType 是加载器标识：fabric / forge / neoforge（预留）。
	LoaderType string
	// LoaderVersion 是加载器版本；Forge 通常形如 1.20.1-47.1.0。
	LoaderVersion string
}
