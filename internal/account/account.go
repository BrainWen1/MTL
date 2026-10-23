// Package account 管理离线账号：用户名校验、离线 UUID 计算与本地持久化。
// 离线 UUID 与 Minecraft Java 版的 UUID.nameUUIDFromBytes(("OfflinePlayer:"+name)) 一致。
package account

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// OfflineFileName 是账号配置在账号目录中的文件名。
const OfflineFileName = "offline.json"

// namePattern 与 Minecraft 原版用户名规则一致：3-16 位字母、数字或下划线。
var namePattern = regexp.MustCompile(`^[A-Za-z0-9_]{3,16}$`)

// ValidateName 校验用户名是否满足 Minecraft 原版规则。
func ValidateName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("用户名 %q 非法：仅允许 3-16 位字母、数字或下划线", name)
	}
	return nil
}

// OfflineUUID 按离线模式算法生成带连字符的规范 UUID。
// 同一个用户名每次得到的结果完全一致，适合局域网联机时保持身份稳定。
func OfflineUUID(name string) string {
	h := md5.Sum([]byte("OfflinePlayer:" + name))
	h[6] = (h[6] & 0x0f) | 0x30 // version 3
	h[8] = (h[8] & 0x3f) | 0x80 // variant RFC 4122
	raw := hex.EncodeToString(h[:])
	return raw[0:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:]
}

// NormalizeUUID 接受带或不带连字符的 UUID，返回小写带连字符形式。
func NormalizeUUID(s string) (string, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	t = strings.ReplaceAll(t, "-", "")
	if len(t) != 32 {
		return "", fmt.Errorf("UUID %q 非法", s)
	}
	for _, r := range t {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return "", fmt.Errorf("UUID %q 非法", s)
		}
	}
	return t[0:8] + "-" + t[8:12] + "-" + t[12:16] + "-" + t[16:20] + "-" + t[20:], nil
}
