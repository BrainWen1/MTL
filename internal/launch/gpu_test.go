package launch

import (
	"reflect"
	"testing"
)

func TestEnsureD3DArg(t *testing.T) {
	base := []string{"-Xmx2G", "-Dfoo=1"}

	got, added := ensureD3DArg(base, false)
	if added || !reflect.DeepEqual(got, base) {
		t.Fatalf("未启用时不应追加: added=%v got=%v", added, got)
	}

	got, added = ensureD3DArg(base, true)
	if !added {
		t.Fatal("启用时应追加 D3D 参数")
	}
	if len(got) != len(base)+1 || got[len(got)-1] != d3dJVMArg {
		t.Fatalf("D3D 参数位置或内容错误: %v", got)
	}

	// 重复执行不产生重复参数。
	again, added := ensureD3DArg(got, true)
	if added || !reflect.DeepEqual(again, got) {
		t.Fatalf("重复追加应被去重: added=%v got=%v", added, again)
	}

	// 用户已在 jvm_args 中写了相同参数时不再追加。
	user := []string{d3dJVMArg, "-Dfoo=1"}
	got, added = ensureD3DArg(user, true)
	if added || !reflect.DeepEqual(got, user) {
		t.Fatalf("用户参数已存在时不应重复追加: added=%v got=%v", added, got)
	}
}
