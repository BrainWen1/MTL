package mrpack

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
)

const (
	IndexFileName          = "modrinth.index.json"
	supportedFormatVersion = 1
	maxIndexBytes          = 8 << 20
)

const (
	EnvRequired    = "required"
	EnvOptional    = "optional"
	EnvUnsupported = "unsupported"
)

const (
	DepMinecraft    = "minecraft"
	DepFabricLoader = "fabric-loader"
	DepQuiltLoader  = "quilt-loader"
	DepForge        = "forge"
	depNeoForge     = "neoforge"
)

type LoaderType string

const (
	LoaderFabric LoaderType = "fabric"
	LoaderQuilt  LoaderType = "quilt"
	LoaderForge  LoaderType = "forge"
)

type loaderDep struct {
	key string
	typ LoaderType
}

var loaderDeps = []loaderDep{
	{DepFabricLoader, LoaderFabric},
	{DepQuiltLoader, LoaderQuilt},
	{DepForge, LoaderForge},
}

type Index struct {
	FormatVersion int               `json:"formatVersion"`
	Game          string            `json:"game"`
	VersionID     string            `json:"versionId"`
	Name          string            `json:"name"`
	Summary       string            `json:"summary"`
	Files         []File            `json:"files"`
	Dependencies  map[string]string `json:"dependencies"`
}

type File struct {
	Path      string   `json:"path"`
	Hashes    Hashes   `json:"hashes"`
	Env       *Env     `json:"env,omitempty"`
	Downloads []string `json:"downloads"`
	FileSize  int64    `json:"fileSize"`
}

type Hashes struct {
	SHA1   string `json:"sha1"`
	SHA512 string `json:"sha512"`
}

type Env struct {
	Client string `json:"client"`
	Server string `json:"server"`
}

// IsClientFile 表示该文件需要写进客户端实例；env 缺省等价于两端都 required。
func (f File) IsClientFile() bool {
	return f.Env == nil || f.Env.Client != EnvUnsupported
}

func (idx *Index) ClientFiles() []File {
	out := make([]File, 0, len(idx.Files))
	for _, f := range idx.Files {
		if f.IsClientFile() {
			out = append(out, f)
		}
	}
	return out
}

// Loader 返回解析阶段已校验过的 Loader 类型和版本。
func (idx *Index) Loader() (LoaderType, string) {
	for _, d := range loaderDeps {
		if v, ok := idx.Dependencies[d.key]; ok {
			return d.typ, v
		}
	}
	return "", ""
}

func ParseIndex(data []byte) (*Index, error) {
	if len(data) > maxIndexBytes {
		return nil, fmt.Errorf("%s 超过 %d 字节上限", IndexFileName, maxIndexBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	var idx Index
	if err := dec.Decode(&idx); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", IndexFileName, err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("%s 末尾存在多余数据", IndexFileName)
	}
	if err := idx.validate(); err != nil {
		return nil, err
	}
	return &idx, nil
}

func (idx *Index) validate() error {
	if idx.FormatVersion != supportedFormatVersion {
		return fmt.Errorf("不支持的 formatVersion %d，仅支持 %d", idx.FormatVersion, supportedFormatVersion)
	}
	if idx.Game != "minecraft" {
		return fmt.Errorf("不支持的 game %q，仅支持 minecraft", idx.Game)
	}
	if idx.VersionID == "" {
		return fmt.Errorf("缺少 versionId")
	}
	if idx.Name == "" {
		return fmt.Errorf("缺少 name")
	}
	if idx.Files == nil {
		return fmt.Errorf("缺少 files")
	}
	if idx.Dependencies == nil {
		return fmt.Errorf("缺少 dependencies")
	}
	if idx.Dependencies[DepMinecraft] == "" {
		return fmt.Errorf("dependencies 缺少 minecraft 版本")
	}
	if _, ok := idx.Dependencies[depNeoForge]; ok {
		return fmt.Errorf("暂不支持 NeoForge")
	}

	var loader *loaderDep
	for i := range loaderDeps {
		if _, ok := idx.Dependencies[loaderDeps[i].key]; !ok {
			continue
		}
		if loader != nil {
			return fmt.Errorf("dependencies 同时包含多个 Loader")
		}
		loader = &loaderDeps[i]
	}
	if loader == nil {
		return fmt.Errorf("dependencies 缺少 Loader（fabric-loader/quilt-loader/forge）")
	}
	if idx.Dependencies[loader.key] == "" {
		return fmt.Errorf("Loader 版本为空")
	}

	seen := make(map[string]string, len(idx.Files))
	for i := range idx.Files {
		f := &idx.Files[i]
		if err := f.validate(); err != nil {
			return fmt.Errorf("files[%d] (%q): %w", i, f.Path, err)
		}
		key := strings.ToLower(f.Path)
		if prev, ok := seen[key]; ok {
			return fmt.Errorf("files 路径重复（Windows 下大小写不敏感）: %q 与 %q", prev, f.Path)
		}
		seen[key] = f.Path
	}
	return nil
}

func (f *File) validate() error {
	if err := ValidateRelPath(f.Path); err != nil {
		return fmt.Errorf("非法路径: %w", err)
	}
	if err := validateHex(f.Hashes.SHA512, 64); err != nil {
		return fmt.Errorf("sha512 无效: %w", err)
	}
	if f.Hashes.SHA1 != "" {
		if err := validateHex(f.Hashes.SHA1, 20); err != nil {
			return fmt.Errorf("sha1 无效: %w", err)
		}
	}
	if len(f.Downloads) == 0 {
		return fmt.Errorf("downloads 为空")
	}
	for _, raw := range f.Downloads {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("非法下载地址 %q", raw)
		}
	}
	if f.FileSize < 0 {
		return fmt.Errorf("fileSize 不能为负数")
	}
	if f.Env != nil {
		if !validEnvValue(f.Env.Client) {
			return fmt.Errorf("env.client 非法: %q", f.Env.Client)
		}
		if !validEnvValue(f.Env.Server) {
			return fmt.Errorf("env.server 非法: %q", f.Env.Server)
		}
	}
	return nil
}

func validEnvValue(v string) bool {
	switch v {
	case EnvRequired, EnvOptional, EnvUnsupported:
		return true
	default:
		return false
	}
}

func validateHex(s string, wantBytes int) error {
	if s == "" {
		return errors.New("为空")
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return errors.New("不是合法十六进制")
	}
	if len(b) != wantBytes {
		return fmt.Errorf("长度应为 %d 字节", wantBytes)
	}
	return nil
}
