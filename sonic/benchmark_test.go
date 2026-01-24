package sonic_test

import (
	"encoding/json"
	"testing"

	"github.com/bytedance/sonic"
)

// 测试用的数据结构
type User struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Email    string   `json:"email"`
	Age      int      `json:"age"`
	Tags     []string `json:"tags"`
	Metadata map[string]interface{} `json:"metadata"`
}

type ComplexData struct {
	Users    []User                 `json:"users"`
	Total    int                    `json:"total"`
	Page     int                    `json:"page"`
	Settings map[string]interface{} `json:"settings"`
}

// 生成测试数据
func generateTestData() ComplexData {
	users := make([]User, 100)
	for i := 0; i < 100; i++ {
		users[i] = User{
			ID:    int64(i),
			Name:  "User" + string(rune(i)),
			Email: "user" + string(rune(i)) + "@example.com",
			Age:   20 + i%50,
			Tags:  []string{"tag1", "tag2", "tag3"},
			Metadata: map[string]interface{}{
				"key1": "value1",
				"key2": 123,
				"key3": true,
			},
		}
	}

	return ComplexData{
		Users: users,
		Total: 100,
		Page:  1,
		Settings: map[string]interface{}{
			"theme":    "dark",
			"language": "en",
			"timezone": "UTC",
		},
	}
}

// ================== 序列化基准测试 ==================

func BenchmarkMarshal_StdJSON(b *testing.B) {
	data := generateTestData()
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := json.Marshal(data)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMarshal_Sonic(b *testing.B) {
	data := generateTestData()
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := sonic.Marshal(data)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// ================== 反序列化基准测试 ==================

func BenchmarkUnmarshal_StdJSON(b *testing.B) {
	data := generateTestData()
	jsonData, _ := json.Marshal(data)
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		var result ComplexData
		err := json.Unmarshal(jsonData, &result)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUnmarshal_Sonic(b *testing.B) {
	data := generateTestData()
	jsonData, _ := sonic.Marshal(data)
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		var result ComplexData
		err := sonic.Unmarshal(jsonData, &result)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// ================== 小对象基准测试 ==================

func BenchmarkMarshalSmall_StdJSON(b *testing.B) {
	user := User{
		ID:    1,
		Name:  "Test User",
		Email: "test@example.com",
		Age:   30,
	}
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := json.Marshal(user)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMarshalSmall_Sonic(b *testing.B) {
	user := User{
		ID:    1,
		Name:  "Test User",
		Email: "test@example.com",
		Age:   30,
	}
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := sonic.Marshal(user)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// ================== 大 JSON 部分读取测试 ==================

func BenchmarkPartialRead_StdJSON(b *testing.B) {
	data := generateTestData()
	jsonData, _ := json.Marshal(data)
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		var result ComplexData
		// 标准库必须完整解析
		err := json.Unmarshal(jsonData, &result)
		if err != nil {
			b.Fatal(err)
		}
		// 只访问一个字段
		_ = result.Total
	}
}

func BenchmarkPartialRead_SonicGet(b *testing.B) {
	data := generateTestData()
	jsonData, _ := sonic.Marshal(data)
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Sonic 可以懒加载，只解析需要的字段
		root, err := sonic.Get(jsonData)
		if err != nil {
			b.Fatal(err)
		}
		node, _ := root.Get("total").Int64()
		_ = node
	}
}

// ================== 字符串转义测试 ==================

func BenchmarkStringEscape_StdJSON(b *testing.B) {
	// 包含需要转义的字符
	str := struct {
		Text string `json:"text"`
	}{
		Text: `This is a "test" with <special> characters & symbols\n\t`,
	}
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := json.Marshal(str)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStringEscape_Sonic(b *testing.B) {
	str := struct {
		Text string `json:"text"`
	}{
		Text: `This is a "test" with <special> characters & symbols\n\t`,
	}
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := sonic.Marshal(str)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// ================== 并发场景测试 ==================

func BenchmarkConcurrent_StdJSON(b *testing.B) {
	data := generateTestData()
	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := json.Marshal(data)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkConcurrent_Sonic(b *testing.B) {
	data := generateTestData()
	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := sonic.Marshal(data)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

// ================== 内存分配对比测试 ==================

func BenchmarkMemoryAlloc_StdJSON(b *testing.B) {
	users := make([]User, 1000)
	for i := range users {
		users[i] = User{
			ID:    int64(i),
			Name:  "User",
			Email: "user@example.com",
			Age:   30,
		}
	}
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := json.Marshal(users)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMemoryAlloc_Sonic(b *testing.B) {
	users := make([]User, 1000)
	for i := range users {
		users[i] = User{
			ID:    int64(i),
			Name:  "User",
			Email: "user@example.com",
			Age:   30,
		}
	}
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := sonic.Marshal(users)
		if err != nil {
			b.Fatal(err)
		}
	}
}
