package launch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"mtl/internal/mrpack"
)

type LibraryFile struct {
	Name      string
	Jar       string // classpath 主构件，可为空
	NativeJar string // 需要解压的 native jar，可为空
}

type Launch struct {
	Dir         string
	ProfileID   string
	GameVersion string
	MainClass   string
	ClientJar   string
	Libraries   []LibraryFile
	Mods        []string
	Vanilla     *Version
	Profile     *Version
	JavaMajor   int
}

// Load 读取 instances/versions 下的 Fabric profile 与 vanilla 版本 json。
func Load(instanceDir string) (*Launch, error) {
	return load(instanceDir, runtime.GOOS, runtime.GOARCH)
}

func load(instanceDir, goos, goarch string) (*Launch, error) {
	if instanceDir == "" {
		return nil, errors.New("实例目录为空")
	}
	abs, err := filepath.Abs(instanceDir)
	if err != nil {
		return nil, fmt.Errorf("解析实例目录失败: %w", err)
	}
	profile, err := findProfile(abs)
	if err != nil {
		return nil, err
	}
	gameVersion := profile.InheritsFrom
	if err := mrpack.ValidateRelPath(gameVersion); err != nil {
		return nil, fmt.Errorf("Fabric profile 的 inheritsFrom %q 非法: %w", gameVersion, err)
	}

	vanillaPath, err := mrpack.SafeJoin(abs, "versions/"+gameVersion+"/"+gameVersion+".json")
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(vanillaPath)
	if err != nil {
		return nil, fmt.Errorf("读取 vanilla 版本 json 失败: %w", err)
	}
	vanilla, err := ParseVersion(raw)
	if err != nil {
		return nil, fmt.Errorf("解析 vanilla 版本 json 失败: %w", err)
	}
	if vanilla.MainClass == "" {
		return nil, errors.New("vanilla 版本 json 缺少 mainClass")
	}

	mainClass := profile.MainClass
	if mainClass == "" {
		mainClass = vanilla.MainClass
	}
	clientJar, err := mrpack.SafeJoin(abs, "versions/"+gameVersion+"/"+gameVersion+".jar")
	if err != nil {
		return nil, err
	}
	libs, err := resolveLibraries(abs, goos, goarch, vanilla.Libraries, profile.Libraries)
	if err != nil {
		return nil, err
	}
	mods, err := scanMods(filepath.Join(abs, "mods"))
	if err != nil {
		return nil, err
	}

	javaMajor := 0
	if vanilla.JavaVersion != nil {
		javaMajor = vanilla.JavaVersion.MajorVersion
	}

	return &Launch{
		Dir:         abs,
		ProfileID:   profile.ID,
		GameVersion: gameVersion,
		MainClass:   mainClass,
		ClientJar:   clientJar,
		Libraries:   libs,
		Mods:        mods,
		Vanilla:     vanilla,
		Profile:     profile,
		JavaMajor:   javaMajor,
	}, nil
}

// findProfile 在 versions/*/*.json 中寻找唯一的、父版本存在的 inheritsFrom profile。
func findProfile(instanceDir string) (*Version, error) {
	versionsDir := filepath.Join(instanceDir, "versions")
	entries, err := os.ReadDir(versionsDir)
	if err != nil {
		return nil, fmt.Errorf("读取 versions 目录失败: %w", err)
	}

	var candidates []*Version
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if mrpack.ValidateRelPath(name) != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(versionsDir, name, name+".json"))
		if err != nil {
			continue
		}
		v, err := ParseVersion(data)
		if err != nil || v.ID == "" || v.InheritsFrom == "" {
			continue
		}
		if mrpack.ValidateRelPath(v.InheritsFrom) != nil {
			continue
		}
		parent := filepath.Join(versionsDir, v.InheritsFrom, v.InheritsFrom+".json")
		if _, err := os.Stat(parent); err != nil {
			continue
		}
		candidates = append(candidates, v)
	}

	switch len(candidates) {
	case 0:
		return nil, errors.New("未找到可用的 Fabric 版本 json（需要 versions/<id>/<id>.json 含 inheritsFrom，且父版本文件存在）")
	case 1:
		return candidates[0], nil
	default:
		ids := make([]string, 0, len(candidates))
		for _, v := range candidates {
			ids = append(ids, v.ID)
		}
		sort.Strings(ids)
		return nil, fmt.Errorf("实例中存在多个可启动的 loader profile: %s", strings.Join(ids, ", "))
	}
}

func resolveLibraries(instanceDir, goos, goarch string, lists ...[]Library) ([]LibraryFile, error) {
	osName, archName := ruleOS(goos, goarch)
	seenJar := make(map[string]bool)
	seenNative := make(map[string]bool)
	var out []LibraryFile

	for _, libs := range lists {
		for _, lib := range libs {
			if !rulesAllow(lib.Rules, osName, archName) {
				continue
			}
			classifier := libraryClassifier(lib.Name)
			arch, isNative := nativeClassifierArch(classifier)
			if isNative && arch != goarch {
				continue
			}

			var jarRel string
			switch {
			case lib.Downloads.Artifact != nil && lib.Downloads.Artifact.Path != "":
				jarRel = "libraries/" + lib.Downloads.Artifact.Path
			case lib.URL != "" && lib.Name != "":
				rel, err := mavenRelPath(lib.Name, "")
				if err != nil {
					return nil, fmt.Errorf("library %q: %w", lib.Name, err)
				}
				jarRel = rel
			}

			var nativeRel string
			if isNative {
				nativeRel = jarRel
			} else if len(lib.Natives) > 0 {
				if cn, ok := lib.Natives[osName]; ok {
					cn = strings.ReplaceAll(cn, "${arch}", archBits(goarch))
					if a, n := nativeClassifierArch(cn); n && a != goarch {
						// 本机架构不匹配，忽略该 native。
					} else if art, ok := lib.Downloads.Classifiers[cn]; ok && art.Path != "" {
						nativeRel = "libraries/" + art.Path
					} else if lib.URL != "" && lib.Name != "" {
						rel, err := mavenRelPath(lib.Name, cn)
						if err != nil {
							return nil, fmt.Errorf("library %q native %q: %w", lib.Name, cn, err)
						}
						nativeRel = rel
					}
				}
			}

			var lf LibraryFile
			lf.Name = lib.Name
			if jarRel != "" {
				p, err := mrpack.SafeJoin(instanceDir, jarRel)
				if err != nil {
					return nil, fmt.Errorf("library %q 路径非法: %w", lib.Name, err)
				}
				if !seenJar[p] {
					seenJar[p] = true
					lf.Jar = p
				}
			}
			if nativeRel != "" {
				p, err := mrpack.SafeJoin(instanceDir, nativeRel)
				if err != nil {
					return nil, fmt.Errorf("library %q native 路径非法: %w", lib.Name, err)
				}
				if !seenNative[p] {
					seenNative[p] = true
					lf.NativeJar = p
				}
			}
			if lf.Jar != "" || lf.NativeJar != "" {
				out = append(out, lf)
			}
		}
	}
	return out, nil
}

func scanMods(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 mods 目录失败: %w", err)
	}
	var mods []string
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".jar") {
			continue
		}
		mods = append(mods, filepath.Join(dir, e.Name()))
	}
	sort.Strings(mods)
	return mods, nil
}

// ClassPathEntries 顺序：client jar → 所有 libraries → mods。
func (l *Launch) ClassPathEntries() []string {
	entries := make([]string, 0, len(l.Libraries)+len(l.Mods)+1)
	entries = append(entries, l.ClientJar)
	for _, lib := range l.Libraries {
		if lib.Jar != "" {
			entries = append(entries, lib.Jar)
		}
	}
	entries = append(entries, l.Mods...)
	return entries
}

func (l *Launch) ClassPath() string {
	return strings.Join(l.ClassPathEntries(), string(os.PathListSeparator))
}

func (l *Launch) nativeJars() []string {
	var jars []string
	for _, lib := range l.Libraries {
		if lib.NativeJar != "" {
			jars = append(jars, lib.NativeJar)
		}
	}
	return jars
}
