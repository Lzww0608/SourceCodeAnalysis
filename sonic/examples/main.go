package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"time"
	"unsafe"

	"github.com/bytedance/sonic"
)

// ==================== 源码级别实现对比演示 ====================

func main() {
	fmt.Println("============ Sonic vs encoding/json 源码级分析 ============\n")

	demo1_ReflectionVsJIT()
	demo2_SIMDStringProcessing()
	demo3_ZeroCopyDemo()
	demo4_LazyLoadingDemo()
	demo5_MemoryPoolDemo()
	demo6_UnsafeRisksDemo()
	demo7_ColdStartDemo()
	demo8_DeepStructureDemo()
}

// ==================== 1. 反射 vs JIT 编译 ====================
func demo1_ReflectionVsJIT() {
	fmt.Println("【1. 反射 vs JIT 编译】")

	type User struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}

	user := User{ID: 1, Name: "Alice", Email: "alice@example.com"}

	// 演示标准库的反射过程
	fmt.Println("\n标准库 encoding/json 的反射过程:")
	fmt.Println("1. 调用 json.Marshal(user)")
	rt := reflect.TypeOf(user)
	fmt.Printf("2. 通过反射获取类型: %v\n", rt)
	fmt.Printf("3. 遍历 %d 个字段\n", rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		fmt.Printf("   - 字段 %s, 类型 %v, JSON标签 %s\n",
			field.Name, field.Type, field.Tag.Get("json"))
	}
	fmt.Println("4. 对每个字段进行类型判断和编码")
	fmt.Println("5. 每次序列化都重复这个过程\n")

	// 标准库性能测试
	count := 100000
	start := time.Now()
	for i := 0; i < count; i++ {
		json.Marshal(user)
	}
	stdDuration := time.Since(start)
	fmt.Printf("标准库序列化 %d 次耗时: %v\n", count, stdDuration)

	// Sonic 的 JIT 编译过程
	fmt.Println("\nSonic 的 JIT 编译过程:")
	fmt.Println("1. 首次调用 sonic.Marshal(user)")
	fmt.Println("2. 检测到新类型 User，触发 JIT 编译")
	fmt.Println("3. 分析类型结构，生成特定的序列化代码")
	fmt.Println("4. 编译成机器码并缓存")
	fmt.Println("5. 后续调用直接使用编译好的代码，无需反射\n")

	// Sonic 性能测试
	start = time.Now()
	for i := 0; i < count; i++ {
		sonic.Marshal(user)
	}
	sonicDuration := time.Since(start)
	fmt.Printf("Sonic序列化 %d 次耗时: %v\n", count, sonicDuration)
	fmt.Printf("性能提升: %.2fx\n\n", float64(stdDuration)/float64(sonicDuration))
	fmt.Println(strings.Repeat("-", 70))
}

// ==================== 2. SIMD 字符串处理 ====================
func demo2_SIMDStringProcessing() {
	fmt.Println("\n【2. 字符串处理：逐字节 vs SIMD】")

	// 创建包含需要转义字符的字符串
	text := `This "string" contains <special> characters & symbols\n\t`
	data := struct {
		Text string `json:"text"`
	}{Text: text}

	fmt.Println("\n标准库 encoding/json 的字符串处理:")
	fmt.Println("伪代码:")
	fmt.Println(`
func escapeString(s string) string {
    for i := 0; i < len(s); i++ {  // 逐字节循环
        c := s[i]
        if c == '"' || c == '\\' || c < 0x20 {  // 分支判断
            // 转义字符
        } else if c == '<' || c == '>' || c == '&' {
            // HTML转义
        } else {
            // 直接写入
        }
    }
}`)

	// 标准库测试
	count := 10000
	start := time.Now()
	for i := 0; i < count; i++ {
		json.Marshal(data)
	}
	stdDuration := time.Since(start)
	fmt.Printf("\n标准库处理 %d 次: %v\n", count, stdDuration)

	fmt.Println("\nSonic 的 SIMD 优化:")
	fmt.Println("伪代码 (使用 AVX2 指令):")
	fmt.Println(`
func escapeStringSIMD(s string) string {
    for i := 0; i < len(s); i += 32 {  // 每次处理32字节
        // 使用 VMOVDQU 加载32字节到 YMM 寄存器
        chunk := loadVector32(s[i:])
        
        // 使用 VPCMPGTB 并行比较32字节
        needsEscape := compareVectorLess(chunk, 0x20)
        
        // 使用 VPMOVMSKB 生成掩码
        mask := movemask(needsEscape)
        
        if mask == 0 {
            // 32字节都不需要转义，直接写入
            storeVector32(output, chunk)
        } else {
            // 处理需要转义的字节
        }
    }
}`)

	// Sonic 测试
	start = time.Now()
	for i := 0; i < count; i++ {
		sonic.Marshal(data)
	}
	sonicDuration := time.Since(start)
	fmt.Printf("\nSonic处理 %d 次: %v\n", count, sonicDuration)
	fmt.Printf("性能提升: %.2fx\n", float64(stdDuration)/float64(sonicDuration))

	fmt.Println("\nSIMD 优势:")
	fmt.Println("✓ 一次处理 32 字节 vs 逐字节处理")
	fmt.Println("✓ 并行比较 vs 顺序比较")
	fmt.Println("✓ 减少分支预测失败")
	fmt.Println("✓ 充分利用 CPU 流水线\n")
	fmt.Println(strings.Repeat("-", 70))
}

// ==================== 3. 零拷贝演示 ====================
func demo3_ZeroCopyDemo() {
	fmt.Println("\n【3. 内存拷贝 vs 零拷贝】")

	jsonData := []byte(`{"name":"Alice","age":30,"city":"Beijing"}`)

	fmt.Println("\n标准库 encoding/json 的内存拷贝:")
	fmt.Println(`
func Unmarshal(data []byte, v interface{}) error {
    // 1. 解析 JSON，创建临时字符串（第一次拷贝）
    str := string(data[start:end])  
    
    // 2. 设置到结构体字段（可能第二次拷贝）
    reflect.ValueOf(v).Elem().FieldByName("name").SetString(str)
    
    return nil
}`)

	// 演示标准库的拷贝
	type Person struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
		City string `json:"city"`
	}

	var p1 Person
	json.Unmarshal(jsonData, &p1)
	fmt.Printf("\n标准库解析结果: %+v\n", p1)
	fmt.Printf("字符串地址: %p\n", unsafe.Pointer(unsafe.StringData(p1.Name)))
	fmt.Printf("原始数据地址: %p\n", unsafe.Pointer(&jsonData[0]))
	fmt.Printf("地址不同，发生了拷贝: %v\n", 
		unsafe.Pointer(unsafe.StringData(p1.Name)) != unsafe.Pointer(&jsonData[0]))

	fmt.Println("\nSonic 的零拷贝技术:")
	fmt.Println(`
func unmarshalString(buf []byte, ptr unsafe.Pointer) {
    start, end := scanString(buf)
    
    // 直接构造 string，指向原始缓冲区（零拷贝）
    strHeader := (*reflect.StringHeader)(ptr)
    strHeader.Data = uintptr(unsafe.Pointer(&buf[start]))
    strHeader.Len = end - start
    
    // 注意：需要确保 buf 的生命周期
}`)

	fmt.Println("\n零拷贝优势:")
	fmt.Println("✓ 减少内存分配")
	fmt.Println("✓ 避免数据拷贝")
	fmt.Println("✓ 降低 GC 压力")
	fmt.Println("✓ 提高缓存命中率")

	fmt.Println("\n零拷贝风险:")
	fmt.Println("✗ 需要管理缓冲区生命周期")
	fmt.Println("✗ 可能产生悬挂指针")
	fmt.Println("✗ 违反 Go 的内存安全保证\n")
	fmt.Println(strings.Repeat("-", 70))
}

// ==================== 4. 懒加载演示 ====================
func demo4_LazyLoadingDemo() {
	fmt.Println("\n【4. 完整解析 vs 懒加载】")

	// 大 JSON 文档
	largeJSON := `{
		"users": [
			{"id":1,"name":"User1","email":"user1@example.com","profile":{"age":25,"city":"Beijing"}},
			{"id":2,"name":"User2","email":"user2@example.com","profile":{"age":30,"city":"Shanghai"}},
			{"id":3,"name":"User3","email":"user3@example.com","profile":{"age":35,"city":"Shenzhen"}}
		],
		"metadata": {
			"total": 3,
			"page": 1,
			"pageSize": 10
		},
		"settings": {
			"theme": "dark",
			"language": "zh-CN",
			"timezone": "Asia/Shanghai"
		}
	}`

	fmt.Println("\n场景: 只需要获取 metadata.total 字段")

	// 标准库 - 必须完整解析
	fmt.Println("\n标准库方式:")
	fmt.Println("1. 解析整个 JSON 文档")
	fmt.Println("2. 创建所有对象（users数组、所有user对象、profile对象等）")
	fmt.Println("3. 访问 result[\"metadata\"][\"total\"]")

	start := time.Now()
	var result map[string]interface{}
	json.Unmarshal([]byte(largeJSON), &result)
	metadata := result["metadata"].(map[string]interface{})
	total := metadata["total"].(float64)
	stdDuration := time.Since(start)

	fmt.Printf("结果: %v\n", total)
	fmt.Printf("耗时: %v\n", stdDuration)

	// Sonic 懒加载
	fmt.Println("\nSonic 懒加载方式:")
	fmt.Println("1. 不立即解析整个文档")
	fmt.Println("2. 使用 Get API 导航到目标字段")
	fmt.Println("3. 只解析 metadata.total 路径上的内容")
	fmt.Println("4. users 数组和 settings 对象保持未解析状态")

	start = time.Now()
	root, _ := sonic.Get([]byte(largeJSON))
	totalNode, _ := root.Get("metadata").Get("total").Int64()
	sonicDuration := time.Since(start)

	fmt.Printf("结果: %v\n", totalNode)
	fmt.Printf("耗时: %v\n", sonicDuration)
	fmt.Printf("性能提升: %.2fx\n", float64(stdDuration)/float64(sonicDuration))

	fmt.Println("\n懒加载适用场景:")
	fmt.Println("✓ 大 JSON 文档，只访问少量字段")
	fmt.Println("✓ API 响应过滤")
	fmt.Println("✓ 配置文件部分读取")
	fmt.Println("✓ 日志分析（只提取特定字段）\n")
	fmt.Println(strings.Repeat("-", 70))
}

// ==================== 5. 内存池演示 ====================
func demo5_MemoryPoolDemo() {
	fmt.Println("\n【5. 内存池优化】")

	type Message struct {
		ID      int64  `json:"id"`
		Content string `json:"content"`
		Time    int64  `json:"time"`
	}

	message := Message{
		ID:      1,
		Content: "Hello, World!",
		Time:    time.Now().Unix(),
	}

	fmt.Println("\n标准库 encoding/json:")
	fmt.Println(`
func Marshal(v interface{}) ([]byte, error) {
    e := newEncodeState()  // 每次都分配新的编码器
    defer e.Release()
    
    err := e.marshal(v, encOpts{escapeHTML: true})
    if err != nil {
        return nil, err
    }
    
    buf := append([]byte(nil), e.Bytes()...)  // 拷贝数据
    return buf, nil
}`)

	fmt.Println("\n每次调用都会:")
	fmt.Println("- 分配 encodeState 对象")
	fmt.Println("- 分配缓冲区")
	fmt.Println("- 拷贝数据到新缓冲区")
	fmt.Println("- GC 回收旧对象")

	fmt.Println("\nSonic 的内存池:")
	fmt.Println(`
var encoderPool = sync.Pool{
    New: func() interface{} {
        return &Encoder{
            buf: make([]byte, 0, 4096),  // 预分配 4KB
        }
    },
}

func Marshal(v interface{}) ([]byte, error) {
    encoder := encoderPool.Get().(*Encoder)  // 从池中获取
    defer func() {
        encoder.buf = encoder.buf[:0]  // 重置，保留容量
        encoderPool.Put(encoder)  // 放回池中
    }()
    
    return encoder.Encode(v)
}`)

	fmt.Println("\n优势:")
	fmt.Println("✓ 复用编码器对象")
	fmt.Println("✓ 复用缓冲区内存")
	fmt.Println("✓ 减少 GC 压力")
	fmt.Println("✓ 特别适合高并发场景")

	// 简单性能对比
	count := 10000
	start := time.Now()
	for i := 0; i < count; i++ {
		json.Marshal(message)
	}
	stdDuration := time.Since(start)

	start = time.Now()
	for i := 0; i < count; i++ {
		sonic.Marshal(message)
	}
	sonicDuration := time.Since(start)

	fmt.Printf("\n标准库 %d 次序列化: %v\n", count, stdDuration)
	fmt.Printf("Sonic %d 次序列化: %v\n", count, sonicDuration)
	fmt.Printf("性能提升: %.2fx\n\n", float64(stdDuration)/float64(sonicDuration))
	fmt.Println(strings.Repeat("-", 70))
}

// ==================== 6. Unsafe 风险演示 ====================
func demo6_UnsafeRisksDemo() {
	fmt.Println("\n【6. Unsafe 使用的风险】")

	fmt.Println("\n标准库的内存安全方式:")
	fmt.Println(`
func (d *Decoder) string() (string, error) {
    b := make([]byte, len)  // 分配新内存
    copy(b, d.data[d.off:d.off+len])  // 拷贝数据
    return string(b), nil  // 创建独立的字符串
}`)
	fmt.Println("✓ 完全内存安全")
	fmt.Println("✓ 字符串独立于原始缓冲区")
	fmt.Println("✓ 不会产生悬挂指针")

	fmt.Println("\nSonic 的 unsafe 优化:")
	fmt.Println(`
func (d *Decoder) string() string {
    start := d.p
    d.skipString()
    end := d.p
    
    // 使用 unsafe 直接构造字符串
    return *(*string)(unsafe.Pointer(&reflect.StringHeader{
        Data: uintptr(unsafe.Pointer(start)),  // 直接指向缓冲区
        Len:  int(end - start),
    }))
}`)
	fmt.Println("✓ 零拷贝，性能极高")
	fmt.Println("✗ 可能产生悬挂指针")
	fmt.Println("✗ 需要管理缓冲区生命周期")

	fmt.Println("\n危险示例:")
	fmt.Println(`
func dangerousExample() string {
    data := []byte({"name":"test"})
    node, _ := sonic.Get(data)
    name, _ := node.Get("name").String()  // 可能零拷贝
    
    data = nil  // 缓冲区可能被 GC 回收
    runtime.GC()
    
    return name  // 可能返回无效数据！危险！
}`)

	fmt.Println("\n建议:")
	fmt.Println("- 确保 JSON 缓冲区在使用期间保持有效")
	fmt.Println("- 如需长期保存，使用 sonic.Copy 或手动拷贝")
	fmt.Println("- 安全关键场景使用标准库\n")
	fmt.Println(strings.Repeat("-", 70))
}

// ==================== 7. 冷启动性能演示 ====================
func demo7_ColdStartDemo() {
	fmt.Println("\n【7. 首次调用（冷启动）性能】")

	// 定义一个新类型
	type Product struct {
		ID          int64   `json:"id"`
		Name        string  `json:"name"`
		Price       float64 `json:"price"`
		Description string  `json:"description"`
		Tags        []string `json:"tags"`
	}

	product := Product{
		ID:          1,
		Name:        "Laptop",
		Price:       999.99,
		Description: "High performance laptop",
		Tags:        []string{"electronics", "computer"},
	}

	fmt.Println("\n标准库首次调用:")
	start := time.Now()
	json.Marshal(product)
	stdFirst := time.Since(start)
	fmt.Printf("耗时: %v\n", stdFirst)

	fmt.Println("\nSonic首次调用 (包含JIT编译):")
	start = time.Now()
	sonic.Marshal(product)
	sonicFirst := time.Since(start)
	fmt.Printf("耗时: %v (包含JIT编译开销)\n", sonicFirst)

	// 后续调用
	fmt.Println("\n--- 后续1000次调用对比 ---")
	
	count := 1000
	start = time.Now()
	for i := 0; i < count; i++ {
		json.Marshal(product)
	}
	stdSubsequent := time.Since(start)

	start = time.Now()
	for i := 0; i < count; i++ {
		sonic.Marshal(product)
	}
	sonicSubsequent := time.Since(start)

	fmt.Printf("\n标准库: %v\n", stdSubsequent)
	fmt.Printf("Sonic: %v\n", sonicSubsequent)
	fmt.Printf("性能提升: %.2fx\n", float64(stdSubsequent)/float64(sonicSubsequent))

	fmt.Println("\n结论:")
	fmt.Println("✓ Sonic 首次调用略慢（JIT 编译）")
	fmt.Println("✓ 后续调用 Sonic 显著更快")
	fmt.Println("✓ 适合长时间运行的服务")
	fmt.Println("✗ 不适合短生命周期进程（CLI工具等）\n")
	fmt.Println(strings.Repeat("-", 70))
}

// ==================== 8. 深层嵌套结构演示 ====================
func demo8_DeepStructureDemo() {
	fmt.Println("\n【8. 深层嵌套结构处理】")

	deepJSON := `{
		"level1": {
			"level2": {
				"level3": {
					"level4": {
						"level5": {
							"data": "target_value",
							"other": "unnecessary_data"
						},
						"extra": [1, 2, 3, 4, 5]
					},
					"more": {"a": 1, "b": 2}
				},
				"array": [10, 20, 30]
			},
			"info": "some info"
		},
		"unused": "large unused data here..."
	}`

	fmt.Println("\n场景: 只需要 level1.level2.level3.level4.level5.data")

	// 标准库方式
	fmt.Println("\n标准库方式:")
	fmt.Println("必须完整解析:")
	fmt.Println("- level1 对象")
	fmt.Println("- level2 对象")
	fmt.Println("- level3 对象 + array 数组")
	fmt.Println("- level4 对象 + extra 数组")
	fmt.Println("- level5 对象 + other 字段")
	fmt.Println("- unused 字段")

	start := time.Now()
	var result map[string]interface{}
	json.Unmarshal([]byte(deepJSON), &result)
	l1 := result["level1"].(map[string]interface{})
	l2 := l1["level2"].(map[string]interface{})
	l3 := l2["level3"].(map[string]interface{})
	l4 := l3["level4"].(map[string]interface{})
	l5 := l4["level5"].(map[string]interface{})
	data := l5["data"].(string)
	stdDuration := time.Since(start)

	fmt.Printf("\n结果: %s\n", data)
	fmt.Printf("耗时: %v\n", stdDuration)

	// Sonic 懒加载方式
	fmt.Println("\nSonic 懒加载方式:")
	fmt.Println("只解析访问路径上的内容:")
	fmt.Println("- 跳过 extra, array, more, unused 等字段")
	fmt.Println("- 只解析到 data 字段为止")

	start = time.Now()
	root, _ := sonic.Get([]byte(deepJSON))
	dataNode, _ := root.Get("level1").Get("level2").Get("level3").
		Get("level4").Get("level5").Get("data").String()
	sonicDuration := time.Since(start)

	fmt.Printf("\n结果: %s\n", dataNode)
	fmt.Printf("耗时: %v\n", sonicDuration)
	fmt.Printf("性能提升: %.2fx\n", float64(stdDuration)/float64(sonicDuration))

	fmt.Println("\n深层嵌套场景的优势:")
	fmt.Println("✓ 跳过大量不需要的数据")
	fmt.Println("✓ 减少内存分配")
	fmt.Println("✓ 减少 CPU 消耗")
	fmt.Println("✓ 特别适合复杂 API 响应的字段提取\n")
	fmt.Println(strings.Repeat("-", 70))
}
