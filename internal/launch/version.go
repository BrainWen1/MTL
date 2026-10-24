// Package launch 负责启动已安装的实例（Fabric / Forge）：解析版本 json、组装
// classpath、解压 native、拼接启动参数并管理 Java 子进程。
package launch

import (
	"encoding/json"
	"fmt"
)

type Version struct {
	ID           string           `json:"id"`
	InheritsFrom string           `json:"inheritsFrom"`
	MainClass    string           `json:"mainClass"`
	Type         string           `json:"type"`
	Libraries    []Library        `json:"libraries"`
	Arguments    Arguments        `json:"arguments"`
	Downloads    VersionDownloads `json:"downloads"`
	JavaVersion  *JavaVersion     `json:"javaVersion"`
	AssetIndex   AssetIndex       `json:"assetIndex"`
	Assets       string           `json:"assets"`
}

type VersionDownloads struct {
	Client Download `json:"client"`
}

type JavaVersion struct {
	Component    string `json:"component"`
	MajorVersion int    `json:"majorVersion"`
}

type AssetIndex struct {
	ID        string `json:"id"`
	SHA1      string `json:"sha1"`
	Size      int64  `json:"size"`
	TotalSize int64  `json:"totalSize"`
	URL       string `json:"url"`
}

type Download struct {
	Path string `json:"path"`
	SHA1 string `json:"sha1"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
}

type Library struct {
	Name      string            `json:"name"`
	URL       string            `json:"url"`
	Downloads LibraryDownloads  `json:"downloads"`
	Natives   map[string]string `json:"natives"`
	Rules     []Rule            `json:"rules"`
}

type LibraryDownloads struct {
	Artifact    *Download           `json:"artifact"`
	Classifiers map[string]Download `json:"classifiers"`
}

type Arguments struct {
	Game []Argument `json:"game"`
	JVM  []Argument `json:"jvm"`
}

// Argument 兼容 arguments 中的字符串和 {rules, value} 两种形式。
type Argument struct {
	Values []string
	Rules  []Rule
}

func (a *Argument) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		a.Values = []string{s}
		return nil
	}
	var obj struct {
		Value json.RawMessage `json:"value"`
		Rules []Rule          `json:"rules"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	if len(obj.Value) == 0 {
		return fmt.Errorf("argument 缺少 value")
	}
	a.Rules = obj.Rules
	if err := json.Unmarshal(obj.Value, &s); err == nil {
		a.Values = []string{s}
		return nil
	}
	if err := json.Unmarshal(obj.Value, &a.Values); err != nil {
		return fmt.Errorf("argument.value 既不是字符串也不是字符串数组")
	}
	return nil
}

type Rule struct {
	Action   string          `json:"action"`
	OS       *OSRule         `json:"os"`
	Features map[string]bool `json:"features"`
}

type OSRule struct {
	Name string `json:"name"`
	Arch string `json:"arch"`
}

func ParseVersion(data []byte) (*Version, error) {
	var v Version
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return &v, nil
}
