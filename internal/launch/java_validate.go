package launch

import "context"

// ValidateJavaPath 校验显式指定的 Java 可执行文件，返回规范化后的绝对路径。
// 复用 run 的 findJava 逻辑：要求路径存在、当前平台可执行，并能正常响应 -version。
// 不校验 Java 版本；版本兼容性仍由实际启动阶段按实例要求判断。
func ValidateJavaPath(ctx context.Context, path string) (string, error) {
	p, _, err := findJava(ctx, 0, path)
	if err != nil {
		return "", err
	}
	return p, nil
}
