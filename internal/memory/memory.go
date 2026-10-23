// Package memory 负责 JVM 堆参数的解析、校验与按物理内存自动推荐。
// 仅处理启动前的静态配置；Java 不支持运行时修改 -Xmx。
package memory

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	MiB = int64(1) << 20
	GiB = int64(1) << 30

	// MinHeap 是允许的最小最大堆。-Xms 同样不会低于该值。
	MinHeap = 256 * MiB
	// DefaultHeap 是未指定 --memory 时的默认最大堆。
	DefaultHeap = 2 * GiB
	// DefaultValue 是未指定 --memory 时的命令行默认值。
	DefaultValue = "2G"

	// Auto 模式下，物理内存不超过 16G 时最多给 6G，超过 16G 时最多给 8G。
	autoThreshold = 16 * GiB
	autoSmallCap  = 6 * GiB
	autoLargeCap  = 8 * GiB
)

// Allocation 是一次启动使用的堆下限（Xms）与上限（Xmx），单位为字节。
type Allocation struct {
	Min int64
	Max int64
}

// Xms 返回可传给 JVM 的 -Xms 值，例如 "512M"。
func (a Allocation) Xms() string { return Format(a.Min) }

// Xmx 返回可传给 JVM 的 -Xmx 值，例如 "4G"。
func (a Allocation) Xmx() string { return Format(a.Max) }

// Parse 解析 "4G"、"512M" 形式的内存值（大小写不敏感，支持小数，结果向下对齐到 MiB）。
// 只做格式解析，最小堆与物理内存上限由 Validate 检查。
func Parse(s string) (int64, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return 0, fmt.Errorf("内存值不能为空，格式应为数字+单位，例如 4G、512M")
	}
	unit := raw[len(raw)-1]
	var multiplier int64
	switch unit {
	case 'G', 'g':
		multiplier = GiB
	case 'M', 'm':
		multiplier = MiB
	default:
		return 0, fmt.Errorf("无效的内存值 %q：格式应为数字+单位，例如 4G、512M", s)
	}

	num := strings.TrimSpace(raw[:len(raw)-1])
	v, err := strconv.ParseFloat(num, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("无效的内存值 %q：格式应为数字+单位，例如 4G、512M", s)
	}
	if v <= 0 {
		return 0, fmt.Errorf("无效的内存值 %q：必须大于 0", s)
	}

	bytes := v * float64(multiplier)
	if bytes > float64(math.MaxInt64) {
		return 0, fmt.Errorf("无效的内存值 %q：超出支持范围", s)
	}
	aligned := int64(bytes) / MiB * MiB
	if aligned <= 0 {
		return 0, fmt.Errorf("无效的内存值 %q：最小单位为 1M", s)
	}
	return aligned, nil
}

// Format 把字节数格式化为 JVM 参数值：整 GiB 用 G，否则用 M。
func Format(size int64) string {
	if size > 0 && size%GiB == 0 {
		return strconv.FormatInt(size/GiB, 10) + "G"
	}
	return strconv.FormatInt((size+MiB-1)/MiB, 10) + "M"
}

// Validate 检查最大堆是否低于 256M 或超过物理内存的 90%。
// total 未知（<=0）时只检查最小值，调用方应给出提示。
func Validate(max, total int64) error {
	if max < MinHeap {
		return fmt.Errorf("内存分配 %s 小于最小值 %s", Format(max), Format(MinHeap))
	}
	if total > 0 {
		limit := total / 10 * 9
		if max > limit {
			return fmt.Errorf("内存分配 %s 超过本机物理内存 %s 的 90%%（上限约 %s），可能因内存不足卡死系统",
				Format(max), Format(total), Format(limit))
		}
	}
	return nil
}

// NewAllocation 按最大堆生成 -Xms/-Xmx 配置：Xms 默认为 Xmx 的 1/4，且不低于 256M。
func NewAllocation(max int64) Allocation {
	min := max / 4
	if min < MinHeap {
		min = MinHeap
	}
	if min > max {
		min = max
	}
	return Allocation{Min: min, Max: max}
}

// AutoMax 按物理内存推荐最大堆：
// 总量 ≤16G 时取 40% 且不超过 6G；大于 16G 时不超过 8G。
// 结果向下对齐到 MiB，并保证不低于 256M（是否超过 90% 仍由 Validate 检查）。
func AutoMax(total int64) int64 {
	if total <= 0 {
		return 0
	}
	cap := autoLargeCap
	if total <= autoThreshold {
		cap = autoSmallCap
	}
	max := total * 4 / 10
	if max > cap {
		max = cap
	}
	max = max / MiB * MiB
	if max < MinHeap {
		max = MinHeap
	}
	return max
}
