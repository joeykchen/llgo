// LITTEST
package main

import "runtime"

// The loop carries the accumulated string and keeps its binary concat path.
// CHECK-LABEL: define {{.*}} @main.concat(
// CHECK: phi %"{{.*}}/runtime/internal/runtime.String"
// CHECK: getelementptr inbounds %"{{.*}}/runtime/internal/runtime.String"
// CHECK: call %"{{.*}}/runtime/internal/runtime.String" @"{{.*}}/runtime/internal/runtime.StringCat"
func concat(args ...string) (ret string) {
	for _, v := range args {
		ret += v
	}
	return
}

// The source conversion may use a local buffer, but the fused result must
// survive this function and later allocations. In particular, neither empty
// operands nor short strings justify returning borrowed input storage.
//
//go:noinline
func fromStack(seed byte) string {
	var bytes [8]byte
	for i := range bytes {
		bytes[i] = seed
	}
	part := string(bytes[:])
	return part + "/" + part
}

// Three parts become one runtime call, including the leading empty string.
// CHECK-LABEL: define {{.*}} @main.info(
// CHECK-NOT: runtime/internal/runtime.StringCat
// CHECK: alloca [3 x %"{{.*}}/runtime/internal/runtime.String"]
// CHECK: call %"{{.*}}/runtime/internal/runtime.String" @"{{.*}}/runtime/internal/runtime.StringConcat"
// CHECK-NOT: runtime/internal/runtime.StringCat
// CHECK: ret %"{{.*}}/runtime/internal/runtime.String"
func info(s string) string {
	return "" + s + "..."
}

func main() {
	result := concat("Hello", " ", "World")
	if info(result) != "Hello World..." {
		panic("fused concat result")
	}
	saved := make([]string, 64)
	for i := range saved {
		saved[i] = fromStack(byte('A' + i%26))
	}
	runtime.GC()
	for i, value := range saved {
		seed := byte('A' + i%26)
		if len(value) != 17 || value[8] != '/' {
			panic("fused concat lifetime")
		}
		for j := 0; j < 8; j++ {
			if value[j] != seed || value[j+9] != seed {
				panic("fused concat input lifetime")
			}
		}
	}
	println(result)
}
