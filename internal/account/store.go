package account

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"mtl/internal/mrpack"
)

// fileVersion 用于后续配置格式升级。
const fileVersion = 1

// ErrNotFound 表示指定的账号不存在。
var ErrNotFound = errors.New("账号不存在")

// Account 是一个离线账号，UUID 在添加时预先算好并持久化。
type Account struct {
	Name string `json:"name"`
	UUID string `json:"uuid"`
}

type accountFile struct {
	Version  int       `json:"version"`
	Accounts []Account `json:"accounts"`
}

// Store 对应 accounts 目录下的 offline.json。
type Store struct {
	path     string
	accounts []Account
}

// DefaultDir 返回平台相关的离线账号目录：
// Windows 为 %APPDATA%/mtl/cache/accounts，其他平台为 ~/.cache/mtl/accounts。
func DefaultDir() (string, error) {
	if runtime.GOOS == "windows" {
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return "", errors.New("未找到 APPDATA 环境变量，无法确定账号缓存目录")
		}
		return filepath.Join(appData, "mtl", "cache", "accounts"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法确定用户主目录: %w", err)
	}
	return filepath.Join(home, ".cache", "mtl", "accounts"), nil
}

// OpenDefault 打开默认账号目录下的配置。
func OpenDefault() (*Store, error) {
	dir, err := DefaultDir()
	if err != nil {
		return nil, err
	}
	return Open(dir)
}

// Open 打开 dir/offline.json；文件不存在时返回空账号列表。
func Open(dir string) (*Store, error) {
	path, err := mrpack.SafeJoin(dir, OfflineFileName)
	if err != nil {
		return nil, fmt.Errorf("账号文件路径非法: %w", err)
	}
	return OpenFile(path)
}

// OpenFile 打开指定账号配置文件。文件损坏时返回错误，调用方可据此回退临时账号。
func OpenFile(path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("解析账号文件路径失败: %w", err)
	}
	raw, err := os.ReadFile(abs)
	if errors.Is(err, os.ErrNotExist) {
		return &Store{path: abs}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取账号文件失败: %w", err)
	}

	var f accountFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("账号文件 %s 损坏: %w", abs, err)
	}
	if f.Version != 0 && f.Version != fileVersion {
		return nil, fmt.Errorf("账号文件 %s 损坏: 不支持的版本 %d", abs, f.Version)
	}

	accounts := make([]Account, 0, len(f.Accounts))
	seen := make(map[string]bool, len(f.Accounts))
	for _, a := range f.Accounts {
		if err := ValidateName(a.Name); err != nil {
			return nil, fmt.Errorf("账号文件 %s 损坏: %w", abs, err)
		}
		uuid, err := NormalizeUUID(a.UUID)
		if err != nil {
			return nil, fmt.Errorf("账号文件 %s 损坏: 账号 %q 的 %w", abs, a.Name, err)
		}
		key := strings.ToLower(a.Name)
		if seen[key] {
			return nil, fmt.Errorf("账号文件 %s 损坏: 账号 %q 重复", abs, a.Name)
		}
		seen[key] = true
		accounts = append(accounts, Account{Name: a.Name, UUID: uuid})
	}
	return &Store{path: abs, accounts: accounts}, nil
}

// Path 返回配置文件的绝对路径。
func (s *Store) Path() string { return s.path }

// List 返回按名称排序的账号副本。
func (s *Store) List() []Account {
	out := append([]Account{}, s.accounts...)
	sort.Slice(out, func(i, j int) bool {
		li, lj := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if li != lj {
			return li < lj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Find 查找账号，优先精确匹配，其次忽略大小写。
func (s *Store) Find(name string) (Account, bool) {
	for _, a := range s.accounts {
		if a.Name == name {
			return a, true
		}
	}
	for _, a := range s.accounts {
		if strings.EqualFold(a.Name, name) {
			return a, true
		}
	}
	return Account{}, false
}

// Add 添加离线账号并立即持久化。账号已存在时返回已有账号且 created=false。
func (s *Store) Add(name string) (acct Account, created bool, err error) {
	if err := ValidateName(name); err != nil {
		return Account{}, false, err
	}
	if a, ok := s.Find(name); ok {
		return a, false, nil
	}
	a := Account{Name: name, UUID: OfflineUUID(name)}
	s.accounts = append(s.accounts, a)
	if err := s.Save(); err != nil {
		s.accounts = s.accounts[:len(s.accounts)-1]
		return Account{}, false, err
	}
	return a, true, nil
}

// Remove 删除账号并立即持久化。
func (s *Store) Remove(name string) (Account, error) {
	idx := -1
	for i, a := range s.accounts {
		if a.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		for i, a := range s.accounts {
			if strings.EqualFold(a.Name, name) {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		return Account{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}

	old := append([]Account{}, s.accounts...)
	removed := s.accounts[idx]
	s.accounts = append(s.accounts[:idx], s.accounts[idx+1:]...)
	if err := s.Save(); err != nil {
		s.accounts = old
		return Account{}, err
	}
	return removed, nil
}

// Save 原子写入账号文件：先写同目录临时文件，再重命名覆盖。
func (s *Store) Save() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建账号目录失败: %w", err)
	}
	accounts := s.accounts
	if accounts == nil {
		accounts = []Account{}
	}
	data, err := json.MarshalIndent(accountFile{Version: fileVersion, Accounts: accounts}, "", "  ")
	if err != nil {
		return fmt.Errorf("编码账号文件失败: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".offline-*.tmp")
	if err != nil {
		return fmt.Errorf("创建账号临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // 成功后 Rename 会失败于不存在，忽略即可

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("设置账号文件权限失败: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入账号文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭账号文件失败: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("保存账号文件失败: %w", err)
	}
	return nil
}
