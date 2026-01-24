package sonic_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"github.com/bytedance/sonic"
)

// ==================== 1. 反射 vs JIT 编译对比 ====================

// 演示标准库的反射开销
func TestStdJSONReflection(t *testing.T) {
	type Product struct {
		ID    int64   `json:"id"`
		Name  string  `json:"name"`
		Price float64 `json:"price"`
	}

	product := Product{ID: 1, Name: "Laptop", Price: 999.99}

	// 标准库每次都要通过反射
	start := time.Now()
	for i := 0; i < 10000; i++ {
		_, _ = json.Marshal(product)
	}
	stdTime := time.Since(start)

	// Sonic 第一次 JIT 编译，后续直接调用
	start = time.Now()
	for i := 0; i < 10000; i++ {
		_, _ = sonic.Marshal(product)
	}
	sonicTime := time.Since(start)

	fmt.Printf("标准库反射方式: %v\n", stdTime)
	fmt.Printf("Sonic JIT方式: %v\n", sonicTime)
	fmt.Printf("性能提升: %.2fx\n", float64(stdTime)/float64(sonicTime))
}

// ==================== 2. 内存拷贝 vs 零拷贝 ====================

// 演示标准库的内存拷贝
func TestMemoryCopy(t *testing.T) {
	jsonStr := `{"name":"test","value":123,"data":"some long string content here"}`

	// 标准库方式 - 多次拷贝
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)

	for i := 0; i < 1000; i++ {
		var result map[string]interface{}
		json.Unmarshal([]byte(jsonStr), &result)
	}

	var m2 runtime.MemStats
	runtime.ReadMemStats(&m2)

	stdAlloc := m2.TotalAlloc - m1.TotalAlloc

	// Sonic 方式 - 减少拷贝
	runtime.ReadMemStats(&m1)

	for i := 0; i < 1000; i++ {
		var result map[string]interface{}
		sonic.Unmarshal([]byte(jsonStr), &result)
	}

	runtime.ReadMemStats(&m2)
	sonicAlloc := m2.TotalAlloc - m1.TotalAlloc

	fmt.Printf("标准库内存分配: %d bytes\n", stdAlloc)
	fmt.Printf("Sonic内存分配: %d bytes\n", sonicAlloc)
	fmt.Printf("内存节省: %.2f%%\n", float64(stdAlloc-sonicAlloc)/float64(stdAlloc)*100)
}

// ==================== 3. 懒加载 vs 完整解析 ====================

func TestLazyLoading(t *testing.T) {
	// 大 JSON 数据
	largeJSON := `{
		"users": [` + generateLargeUserArray(100) + `],
		"metadata": {"total": 100, "page": 1},
		"settings": {"theme": "dark", "lang": "en"}
	}`

	// 标准库 - 必须完整解析
	start := time.Now()
	for i := 0; i < 1000; i++ {
		var result map[string]interface{}
		json.Unmarshal([]byte(largeJSON), &result)
		// 只访问 metadata.total
		_ = result["metadata"].(map[string]interface{})["total"]
	}
	stdTime := time.Since(start)

	// Sonic - 懒加载，只解析需要的部分
	start = time.Now()
	for i := 0; i < 1000; i++ {
		root, _ := sonic.Get([]byte(largeJSON))
		// 只解析 metadata.total 路径
		total, _ := root.Get("metadata").Get("total").Int64()
		_ = total
	}
	sonicTime := time.Since(start)

	fmt.Printf("标准库完整解析: %v\n", stdTime)
	fmt.Printf("Sonic懒加载: %v\n", sonicTime)
	fmt.Printf("性能提升: %.2fx\n", float64(stdTime)/float64(sonicTime))
}

func generateLargeUserArray(count int) string {
	result := ""
	for i := 0; i < count; i++ {
		if i > 0 {
			result += ","
		}
		result += fmt.Sprintf(`{"id":%d,"name":"User%d","email":"user%d@example.com"}`, i, i, i)
	}
	return result
}

// ==================== 4. Unsafe 使用风险演示 ====================

// 演示 Sonic 的零拷贝可能带来的问题
func TestUnsafeRisk(t *testing.T) {
	// 这个测试演示了 unsafe 的潜在风险
	// 注意：这是一个教学示例，不要在生产代码中这样做

	jsonData := []byte(`{"name":"test","value":123}`)

	// 使用 Sonic 的 Get API（可能使用零拷贝）
	root, _ := sonic.Get(jsonData)
	nameNode := root.Get("name")

	// 获取字符串值
	name1, _ := nameNode.String()
	fmt.Printf("第一次读取: %s\n", name1)

	// 修改原始缓冲区
	copy(jsonData, []byte(`{"name":"xxxx","value":456}`))

	// 再次读取 - 在某些实现中可能看到修改后的值
	// 这取决于 Sonic 是否真的使用了零拷贝
	name2, _ := nameNode.String()
	fmt.Printf("修改缓冲区后: %s\n", name2)

	// 标准库总是安全的
	var stdResult map[string]interface{}
	json.Unmarshal([]byte(`{"name":"test","value":123}`), &stdResult)
	stdName := stdResult["name"].(string)
	fmt.Printf("标准库方式: %s (始终安全)\n", stdName)
}

// ==================== 5. 字符串处理：逐字节 vs SIMD ====================

// 演示字符串转义的性能差异
func TestStringEscapePerformance(t *testing.T) {
	// 包含大量需要转义的字符
	complexString := struct {
		Data string `json:"data"`
	}{
		Data: `Line1: "quoted"\nLine2: <html>&amp;</html>\tTab\rReturn\\Backslash`,
	}

	// 重复 1000 次来放大差异
	largeData := make([]struct {
		Data string `json:"data"`
	}, 1000)
	for i := range largeData {
		largeData[i] = complexString
	}

	// 标准库 - 逐字节检查和转义
	start := time.Now()
	_, _ = json.Marshal(largeData)
	stdTime := time.Since(start)

	// Sonic - SIMD 批量处理
	start = time.Now()
	_, _ = sonic.Marshal(largeData)
	sonicTime := time.Since(start)

	fmt.Printf("标准库(逐字节): %v\n", stdTime)
	fmt.Printf("Sonic(SIMD): %v\n", sonicTime)
	fmt.Printf("性能提升: %.2fx\n", float64(stdTime)/float64(sonicTime))
}

// ==================== 6. 首次调用性能对比 ====================

func TestColdStart(t *testing.T) {
	type NewType struct {
		Field1 string
		Field2 int
		Field3 []string
		Field4 map[string]interface{}
	}

	data := NewType{
		Field1: "test",
		Field2: 123,
		Field3: []string{"a", "b", "c"},
		Field4: map[string]interface{}{"key": "value"},
	}

	// 标准库首次调用
	start := time.Now()
	json.Marshal(data)
	stdFirstCall := time.Since(start)

	// Sonic 首次调用（包含 JIT 编译）
	start = time.Now()
	sonic.Marshal(data)
	sonicFirstCall := time.Since(start)

	// 标准库后续调用
	start = time.Now()
	for i := 0; i < 1000; i++ {
		json.Marshal(data)
	}
	stdSubsequent := time.Since(start)

	// Sonic 后续调用（使用编译好的代码）
	start = time.Now()
	for i := 0; i < 1000; i++ {
		sonic.Marshal(data)
	}
	sonicSubsequent := time.Since(start)

	fmt.Printf("=== 首次调用（冷启动）===\n")
	fmt.Printf("标准库: %v\n", stdFirstCall)
	fmt.Printf("Sonic (含JIT): %v\n", sonicFirstCall)
	fmt.Printf("\n=== 1000次后续调用 ===\n")
	fmt.Printf("标准库: %v\n", stdSubsequent)
	fmt.Printf("Sonic: %v\n", sonicSubsequent)
	fmt.Printf("后续调用性能提升: %.2fx\n", float64(stdSubsequent)/float64(sonicSubsequent))
}

// ==================== 7. 自定义 MarshalJSON 支持 ====================

type CustomTime time.Time

func (ct CustomTime) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf(`"%s"`, time.Time(ct).Format("2006-01-02"))), nil
}

func TestCustomMarshalJSON(t *testing.T) {
	type Event struct {
		Name string     `json:"name"`
		Date CustomTime `json:"date"`
	}

	event := Event{
		Name: "Meeting",
		Date: CustomTime(time.Now()),
	}

	// 标准库 - 完美支持
	stdData, _ := json.Marshal(event)
	fmt.Printf("标准库: %s\n", string(stdData))

	// Sonic - 可能需要配置才能正确支持
	sonicData, _ := sonic.Marshal(event)
	fmt.Printf("Sonic: %s\n", string(sonicData))

	// 检查两者是否一致
	if string(stdData) != string(sonicData) {
		t.Logf("注意: Sonic 和标准库对自定义 MarshalJSON 的处理可能不同")
	}
}

// ==================== 8. 内存对齐和缓存友好性 ====================

func TestMemoryLayout(t *testing.T) {
	// 查看结构体的内存布局
	type CompactStruct struct {
		A int64  // 8 bytes
		B int32  // 4 bytes
		C int32  // 4 bytes
	}

	type NonCompactStruct struct {
		A int64 // 8 bytes
		B byte  // 1 byte + 3 bytes padding
		C int32 // 4 bytes
		D byte  // 1 byte + 7 bytes padding
	}

	fmt.Printf("CompactStruct size: %d bytes\n", unsafe.Sizeof(CompactStruct{}))
	fmt.Printf("NonCompactStruct size: %d bytes\n", unsafe.Sizeof(NonCompactStruct{}))

	// Sonic 对内存布局更敏感，紧凑的结构可以更好地利用 SIMD
	compact := make([]CompactStruct, 1000)
	nonCompact := make([]NonCompactStruct, 1000)

	// 测试紧凑结构
	start := time.Now()
	sonic.Marshal(compact)
	compactTime := time.Since(start)

	// 测试非紧凑结构
	start = time.Now()
	sonic.Marshal(nonCompact)
	nonCompactTime := time.Since(start)

	fmt.Printf("Sonic序列化紧凑结构: %v\n", compactTime)
	fmt.Printf("Sonic序列化非紧凑结构: %v\n", nonCompactTime)
}

// ==================== 9. 类型断言性能 ====================

func TestTypeAssertion(t *testing.T) {
	jsonStr := `{"key1": "value1", "key2": 123, "key3": true, "key4": [1,2,3]}`

	// 标准库 - 接口类型断言开销
	start := time.Now()
	for i := 0; i < 10000; i++ {
		var result map[string]interface{}
		json.Unmarshal([]byte(jsonStr), &result)

		// 需要类型断言才能使用
		_ = result["key1"].(string)
		_ = result["key2"].(float64) // JSON 数字默认是 float64
		_ = result["key3"].(bool)
	}
	stdTime := time.Since(start)

	// Sonic 的 Get API - 类型安全的访问
	start = time.Now()
	for i := 0; i < 10000; i++ {
		root, _ := sonic.Get([]byte(jsonStr))

		// 直接获取类型化的值
		_, _ = root.Get("key1").String()
		_, _ = root.Get("key2").Int64()
		_, _ = root.Get("key3").Bool()
	}
	sonicTime := time.Since(start)

	fmt.Printf("标准库(interface{} + 类型断言): %v\n", stdTime)
	fmt.Printf("Sonic(Get API): %v\n", sonicTime)
	fmt.Printf("性能提升: %.2fx\n", float64(stdTime)/float64(sonicTime))
}

// ==================== 10. 并发场景下的内存池效果 ====================

func TestConcurrentMemoryPool(t *testing.T) {
	type Message struct {
		ID      int64  `json:"id"`
		Content string `json:"content"`
		Time    int64  `json:"time"`
	}

	message := Message{
		ID:      1,
		Content: "Test message content",
		Time:    time.Now().Unix(),
	}

	// 标准库 - 每个协程独立分配
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)

	done := make(chan bool)
	for i := 0; i < 100; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				json.Marshal(message)
			}
			done <- true
		}()
	}

	for i := 0; i < 100; i++ {
		<-done
	}

	var m2 runtime.MemStats
	runtime.ReadMemStats(&m2)
	stdAlloc := m2.TotalAlloc - m1.TotalAlloc

	// Sonic - 使用对象池
	runtime.ReadMemStats(&m1)

	for i := 0; i < 100; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				sonic.Marshal(message)
			}
			done <- true
		}()
	}

	for i := 0; i < 100; i++ {
		<-done
	}

	runtime.ReadMemStats(&m2)
	sonicAlloc := m2.TotalAlloc - m1.TotalAlloc

	fmt.Printf("标准库并发内存分配: %d bytes\n", stdAlloc)
	fmt.Printf("Sonic并发内存分配: %d bytes\n", sonicAlloc)
	fmt.Printf("内存节省: %.2f%%\n", float64(stdAlloc-sonicAlloc)/float64(stdAlloc)*100)
}

// ==================== 11. 深度嵌套结构性能 ====================

func TestDeepNesting(t *testing.T) {
	// 创建深度嵌套的结构
	deepJSON := `{"level1":{"level2":{"level3":{"level4":{"level5":{"data":"value"}}}}}}`

	// 标准库 - 完整解析所有层级
	start := time.Now()
	for i := 0; i < 10000; i++ {
		var result map[string]interface{}
		json.Unmarshal([]byte(deepJSON), &result)
	}
	stdTime := time.Since(start)

	// Sonic - 懒加载，按需解析
	start = time.Now()
	for i := 0; i < 10000; i++ {
		root, _ := sonic.Get([]byte(deepJSON))
		_, _ = root.Get("level1").Get("level2").Get("level3").Get("level4").Get("level5").Get("data").String()
	}
	sonicTime := time.Since(start)

	fmt.Printf("标准库(完整解析): %v\n", stdTime)
	fmt.Printf("Sonic(懒加载): %v\n", sonicTime)
	fmt.Printf("性能提升: %.2fx\n", float64(stdTime)/float64(sonicTime))
}

// ==================== 12. 平台检测 ====================

func TestPlatformSupport(t *testing.T) {
	fmt.Printf("当前架构: %s\n", runtime.GOARCH)
	fmt.Printf("当前操作系统: %s\n", runtime.GOOS)

	// Sonic 只在特定平台可用
	supportedArchs := []string{"amd64", "arm64"}
	isSupported := false
	for _, arch := range supportedArchs {
		if runtime.GOARCH == arch {
			isSupported = true
			break
		}
	}

	if isSupported {
		fmt.Printf("✓ Sonic 支持当前平台\n")
	} else {
		fmt.Printf("✗ Sonic 不支持当前平台，将回退到标准库\n")
	}
}

// ==================== 13. 反射深度对比 ====================

func TestReflectionDepth(t *testing.T) {
	type ComplexStruct struct {
		F1  string
		F2  int
		F3  []int
		F4  map[string]interface{}
		F5  *ComplexStruct
		F6  [10]byte
		F7  bool
		F8  float64
		F9  interface{}
		F10 []string
	}

	complex := ComplexStruct{
		F1:  "test",
		F2:  123,
		F3:  []int{1, 2, 3},
		F4:  map[string]interface{}{"key": "value"},
		F5:  nil,
		F6:  [10]byte{},
		F7:  true,
		F8:  3.14,
		F9:  "interface value",
		F10: []string{"a", "b"},
	}

	// 标准库需要深度反射
	rt := reflect.TypeOf(complex)
	fmt.Printf("结构体字段数: %d\n", rt.NumField())
	fmt.Printf("标准库需要反射每个字段的类型、标签等信息\n")

	// Sonic JIT 编译后直接访问
	fmt.Printf("Sonic 在 JIT 编译后生成直接访问代码，避免运行时反射\n")

	// 性能测试
	start := time.Now()
	for i := 0; i < 10000; i++ {
		json.Marshal(complex)
	}
	stdTime := time.Since(start)

	start = time.Now()
	for i := 0; i < 10000; i++ {
		sonic.Marshal(complex)
	}
	sonicTime := time.Since(start)

	fmt.Printf("标准库: %v\n", stdTime)
	fmt.Printf("Sonic: %v\n", sonicTime)
	fmt.Printf("复杂结构性能提升: %.2fx\n", float64(stdTime)/float64(sonicTime))
}
