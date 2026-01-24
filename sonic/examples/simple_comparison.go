package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/bytedance/sonic"
)

type User struct {
	ID       int64             `json:"id"`
	Name     string            `json:"name"`
	Email    string            `json:"email"`
	Age      int               `json:"age"`
	Active   bool              `json:"active"`
	Tags     []string          `json:"tags"`
	Metadata map[string]string `json:"metadata"`
}

func main() {
	fmt.Println("==========================================")
	fmt.Println("  Sonic vs encoding/json 性能对比演示")
	fmt.Println("==========================================\n")

	// 准备测试数据
	user := User{
		ID:     123456,
		Name:   "张三",
		Email:  "zhangsan@example.com",
		Age:    30,
		Active: true,
		Tags:   []string{"golang", "json", "performance"},
		Metadata: map[string]string{
			"department": "engineering",
			"level":      "senior",
			"location":   "beijing",
		},
	}

	// 序列化对比
	fmt.Println("【序列化对比】")
	compareMarshaling(user)

	// 反序列化对比
	fmt.Println("\n【反序列化对比】")
	jsonData, _ := json.Marshal(user)
	compareUnmarshaling(jsonData)

	// 大数据量对比
	fmt.Println("\n【大数据量对比 (1000个用户)】")
	users := make([]User, 1000)
	for i := range users {
		users[i] = User{
			ID:     int64(i),
			Name:   fmt.Sprintf("用户%d", i),
			Email:  fmt.Sprintf("user%d@example.com", i),
			Age:    20 + i%50,
			Active: i%2 == 0,
			Tags:   []string{"tag1", "tag2", "tag3"},
			Metadata: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
		}
	}
	compareLargeData(users)

	// 懒加载对比
	fmt.Println("\n【懒加载特性 (Sonic Get API)】")
	demonstrateLazyLoading()
}

func compareMarshaling(user User) {
	iterations := 100000

	// 标准库测试
	start := time.Now()
	for i := 0; i < iterations; i++ {
		_, _ = json.Marshal(user)
	}
	stdDuration := time.Since(start)

	// Sonic 测试
	start = time.Now()
	for i := 0; i < iterations; i++ {
		_, _ = sonic.Marshal(user)
	}
	sonicDuration := time.Since(start)

	// 结果展示
	fmt.Printf("  迭代次数: %d\n", iterations)
	fmt.Printf("  encoding/json: %v\n", stdDuration)
	fmt.Printf("  sonic:         %v\n", sonicDuration)
	fmt.Printf("  性能提升:      %.2fx\n", float64(stdDuration)/float64(sonicDuration))
	fmt.Printf("  每次操作:      %.2f μs (std) vs %.2f μs (sonic)\n",
		float64(stdDuration.Microseconds())/float64(iterations),
		float64(sonicDuration.Microseconds())/float64(iterations))
}

func compareUnmarshaling(jsonData []byte) {
	iterations := 100000

	// 标准库测试
	start := time.Now()
	for i := 0; i < iterations; i++ {
		var user User
		_ = json.Unmarshal(jsonData, &user)
	}
	stdDuration := time.Since(start)

	// Sonic 测试
	start = time.Now()
	for i := 0; i < iterations; i++ {
		var user User
		_ = sonic.Unmarshal(jsonData, &user)
	}
	sonicDuration := time.Since(start)

	// 结果展示
	fmt.Printf("  迭代次数: %d\n", iterations)
	fmt.Printf("  encoding/json: %v\n", stdDuration)
	fmt.Printf("  sonic:         %v\n", sonicDuration)
	fmt.Printf("  性能提升:      %.2fx\n", float64(stdDuration)/float64(sonicDuration))
	fmt.Printf("  每次操作:      %.2f μs (std) vs %.2f μs (sonic)\n",
		float64(stdDuration.Microseconds())/float64(iterations),
		float64(sonicDuration.Microseconds())/float64(iterations))
}

func compareLargeData(users []User) {
	iterations := 1000

	// 序列化对比
	start := time.Now()
	for i := 0; i < iterations; i++ {
		_, _ = json.Marshal(users)
	}
	stdMarshalDuration := time.Since(start)

	start = time.Now()
	for i := 0; i < iterations; i++ {
		_, _ = sonic.Marshal(users)
	}
	sonicMarshalDuration := time.Since(start)

	// 反序列化对比
	jsonData, _ := json.Marshal(users)

	start = time.Now()
	for i := 0; i < iterations; i++ {
		var result []User
		_ = json.Unmarshal(jsonData, &result)
	}
	stdUnmarshalDuration := time.Since(start)

	start = time.Now()
	for i := 0; i < iterations; i++ {
		var result []User
		_ = sonic.Unmarshal(jsonData, &result)
	}
	sonicUnmarshalDuration := time.Since(start)

	// 结果展示
	fmt.Printf("  数据大小: %d bytes\n", len(jsonData))
	fmt.Printf("  迭代次数: %d\n", iterations)
	fmt.Println("\n  序列化:")
	fmt.Printf("    encoding/json: %v (%.2f ms/op)\n",
		stdMarshalDuration,
		float64(stdMarshalDuration.Milliseconds())/float64(iterations))
	fmt.Printf("    sonic:         %v (%.2f ms/op)\n",
		sonicMarshalDuration,
		float64(sonicMarshalDuration.Milliseconds())/float64(iterations))
	fmt.Printf("    性能提升:      %.2fx\n",
		float64(stdMarshalDuration)/float64(sonicMarshalDuration))

	fmt.Println("\n  反序列化:")
	fmt.Printf("    encoding/json: %v (%.2f ms/op)\n",
		stdUnmarshalDuration,
		float64(stdUnmarshalDuration.Milliseconds())/float64(iterations))
	fmt.Printf("    sonic:         %v (%.2f ms/op)\n",
		sonicUnmarshalDuration,
		float64(sonicUnmarshalDuration.Milliseconds())/float64(iterations))
	fmt.Printf("    性能提升:      %.2fx\n",
		float64(stdUnmarshalDuration)/float64(sonicUnmarshalDuration))
}

func demonstrateLazyLoading() {
	// 大 JSON 文档
	largeJSON := `{
		"users": [
			{"id":1,"name":"User1","email":"user1@example.com","profile":{"bio":"Long bio text...","interests":["coding","reading","music"]}},
			{"id":2,"name":"User2","email":"user2@example.com","profile":{"bio":"Long bio text...","interests":["sports","travel","food"]}},
			{"id":3,"name":"User3","email":"user3@example.com","profile":{"bio":"Long bio text...","interests":["art","gaming","movies"]}}
		],
		"metadata": {
			"total": 3,
			"page": 1,
			"pageSize": 10,
			"timestamp": 1234567890
		},
		"settings": {
			"theme": "dark",
			"language": "zh-CN",
			"notifications": {"email": true, "push": false, "sms": true}
		}
	}`

	iterations := 10000

	// 标准库 - 必须完整解析
	fmt.Println("  场景: 只需要获取 metadata.total 字段")
	start := time.Now()
	for i := 0; i < iterations; i++ {
		var result map[string]interface{}
		json.Unmarshal([]byte(largeJSON), &result)
		metadata := result["metadata"].(map[string]interface{})
		_ = metadata["total"].(float64)
	}
	stdDuration := time.Since(start)

	// Sonic Get API - 懒加载
	start = time.Now()
	for i := 0; i < iterations; i++ {
		root, _ := sonic.Get([]byte(largeJSON))
		_, _ = root.Get("metadata").Get("total").Int64()
	}
	sonicDuration := time.Since(start)

	// 结果展示
	fmt.Printf("  JSON大小: %d bytes\n", len(largeJSON))
	fmt.Printf("  迭代次数: %d\n\n", iterations)
	fmt.Printf("  encoding/json (完整解析): %v\n", stdDuration)
	fmt.Printf("  sonic Get API (懒加载):  %v\n", sonicDuration)
	fmt.Printf("  性能提升:                 %.2fx\n", float64(stdDuration)/float64(sonicDuration))
	fmt.Println("\n  说明:")
	fmt.Println("    - encoding/json 必须解析整个 JSON (users数组、settings对象等)")
	fmt.Println("    - sonic 只解析访问路径 metadata.total，跳过其他部分")
	fmt.Println("    - 数据越大，只访问少量字段时，Sonic 优势越明显")
}
