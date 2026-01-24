# Sonic vs encoding/json 架构对比

## 1. 整体架构对比

```
┌─────────────────────────────────────────────────────────────────┐
│                    encoding/json 架构                            │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  Application Code                                               │
│         │                                                        │
│         ▼                                                        │
│  ┌──────────────────┐                                          │
│  │ json.Marshal()   │                                          │
│  │ json.Unmarshal() │                                          │
│  └────────┬─────────┘                                          │
│           │                                                     │
│           ▼                                                     │
│  ┌──────────────────┐       每次调用都要:                      │
│  │   Reflection     │       • 获取类型信息                    │
│  │   Type Check     │       • 遍历结构体字段                  │
│  │   Field Lookup   │       • 类型判断和转换                  │
│  └────────┬─────────┘       • interface{} 操作                 │
│           │                                                     │
│           ▼                                                     │
│  ┌──────────────────┐                                          │
│  │  String Escape   │       逐字节处理:                        │
│  │  (Byte-by-byte)  │       • for i := 0; i < len(s); i++    │
│  └────────┬─────────┘       • 分支密集                        │
│           │                                                     │
│           ▼                                                     │
│  ┌──────────────────┐                                          │
│  │  Buffer Write    │       频繁分配:                          │
│  │  Memory Alloc    │       • 每次新建编码器                  │
│  └────────┬─────────┘       • 拷贝最终结果                    │
│           │                                                     │
│           ▼                                                     │
│     JSON Output                                                │
│                                                                 │
│  优点: 纯Go、跨平台、稳定、安全                                │
│  缺点: 反射开销、逐字节处理、频繁分配                          │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘


┌─────────────────────────────────────────────────────────────────┐
│                       Sonic 架构                                 │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  Application Code                                               │
│         │                                                        │
│         ▼                                                        │
│  ┌──────────────────┐                                          │
│  │ sonic.Marshal()  │                                          │
│  │sonic.Unmarshal() │                                          │
│  └────────┬─────────┘                                          │
│           │                                                     │
│           ▼                                                     │
│  ┌──────────────────┐       首次调用:                          │
│  │   Type Cache     │───────▶ 类型已编译? ──No──┐            │
│  │   Lookup         │       │                     │            │
│  └────────┬─────────┘       └──Yes────┐          │            │
│           │                            │          ▼            │
│           │                            │   ┌──────────────┐   │
│           │                            │   │ JIT Compiler │   │
│           │                            │   │ • 分析类型   │   │
│           │                            │   │ • 生成代码   │   │
│           │                            │   │ • 编译缓存   │   │
│           │                            │   └──────┬───────┘   │
│           │                            │          │            │
│           │                            └──────────┘            │
│           ▼                                                     │
│  ┌──────────────────┐       直接调用编译好的代码:              │
│  │  Compiled Code   │       • 无反射                          │
│  │  (Type-specific) │       • 直接内存访问                    │
│  └────────┬─────────┘       • 内联优化                        │
│           │                                                     │
│           ▼                                                     │
│  ┌──────────────────┐       SIMD 批量处理:                     │
│  │  String Escape   │       • 一次32字节 (AVX2)               │
│  │  (SIMD-based)    │       • 并行比较                        │
│  └────────┬─────────┘       • 减少分支                        │
│           │                                                     │
│           ▼                                                     │
│  ┌──────────────────┐       零拷贝 + 对象池:                   │
│  │  Memory Pool     │       • 复用编码器                      │
│  │  Zero-copy       │       • 直接引用原始缓冲区              │
│  └────────┬─────────┘       • 减少GC压力                      │
│           │                                                     │
│           ▼                                                     │
│     JSON Output                                                │
│                                                                 │
│  优点: 3-4x性能、懒加载、低内存分配                            │
│  缺点: 平台限制、unsafe、调试难、冷启动                        │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

## 2. 序列化流程对比

### encoding/json 序列化流程
```
User 对象
    │
    ▼
┌─────────────────────┐
│ reflect.TypeOf(v)   │ ← 每次都要反射
└──────────┬──────────┘
           │
           ▼
┌─────────────────────┐
│ v.NumField()        │ ← 遍历所有字段
│ for i := 0..n       │
└──────────┬──────────┘
           │
           ▼
┌─────────────────────┐
│ field.Type.Kind()   │ ← 每个字段类型判断
│ switch kind:        │
│   case String: →    │
│   case Int: →       │
│   case Struct: →    │
└──────────┬──────────┘
           │
           ▼
┌─────────────────────┐
│ String Escape       │
│ for i := 0; i<len {│ ← 逐字节循环
│   if needEscape {  │
│     escape(c)       │
│   }                 │
│ }                   │
└──────────┬──────────┘
           │
           ▼
┌─────────────────────┐
│ Write to Buffer     │ ← 写入缓冲区
└──────────┬──────────┘
           │
           ▼
┌─────────────────────┐
│ Copy to Result      │ ← 拷贝数据
│ buf := append(...)  │
└──────────┬──────────┘
           │
           ▼
      JSON bytes
```

### Sonic 序列化流程
```
User 对象
    │
    ▼
┌─────────────────────┐
│ Type Cache Lookup   │ ← 快速查找
└──────────┬──────────┘
           │
           ├─ Hit ──────────┐
           │                 │
           └─ Miss ─┐        │
                     ▼        │
           ┌──────────────┐  │
           │ JIT Compile  │  │
           │ • Analyze    │  │
           │ • Generate   │  │
           │ • Cache      │  │
           └──────┬───────┘  │
                  │          │
                  └──────────┘
                     │
                     ▼
           ┌──────────────────┐
           │ Execute Compiled │ ← 直接调用
           │ Type-specific    │   无反射
           │ Function         │
           └──────┬───────────┘
                  │
                  ▼
           ┌──────────────────┐
           │ SIMD String      │
           │ VMOVDQU (32B)   │ ← 批量处理
           │ VPCMPGTB        │   并行比较
           │ VPMOVMSKB       │
           └──────┬───────────┘
                  │
                  ▼
           ┌──────────────────┐
           │ Pool Buffer      │ ← 从池获取
           │ Write Direct     │   直接写入
           └──────┬───────────┘
                  │
                  ▼
           ┌──────────────────┐
           │ Return Buffer    │ ← 无需拷贝
           │ (No Copy)        │
           └──────┬───────────┘
                  │
                  ▼
             JSON bytes
```

## 3. 反序列化流程对比

### encoding/json 反序列化流程
```
JSON bytes
    │
    ▼
┌─────────────────────┐
│ Scanner Parse       │ ← 完整扫描
│ Full Parse          │
└──────────┬──────────┘
           │
           ▼
┌─────────────────────┐
│ Create All Objects  │ ← 创建所有对象
│ • Objects           │
│ • Arrays            │
│ • Maps              │
└──────────┬──────────┘
           │
           ▼
┌─────────────────────┐
│ reflect.ValueOf(v)  │ ← 反射目标对象
└──────────┬──────────┘
           │
           ▼
┌─────────────────────┐
│ For Each Field      │
│ • Find field        │ ← 字段匹配
│ • Type convert      │
│ • Set value         │
└──────────┬──────────┘
           │
           ▼
┌─────────────────────┐
│ String Copy         │ ← 拷贝字符串
│ make([]byte, len)   │
│ copy(...)           │
└──────────┬──────────┘
           │
           ▼
      User 对象
```

### Sonic 反序列化流程 (两种模式)

#### 模式1: 标准反序列化
```
JSON bytes
    │
    ▼
┌─────────────────────┐
│ Type Cache Lookup   │
└──────────┬──────────┘
           │
           ▼
┌─────────────────────┐
│ Compiled Decoder    │ ← JIT编译的解码器
│ (Type-specific)     │
└──────────┬──────────┘
           │
           ▼
┌─────────────────────┐
│ Direct Field Access │ ← 直接内存操作
│ (unsafe pointer)    │   无需反射
└──────────┬──────────┘
           │
           ▼
┌─────────────────────┐
│ Zero-copy String    │ ← 零拷贝（可选）
│ Point to buffer     │
└──────────┬──────────┘
           │
           ▼
      User 对象
```

#### 模式2: 懒加载 (Get API)
```
JSON bytes
    │
    ▼
┌─────────────────────┐
│ Create Root Node    │ ← 只创建根节点
│ (No parsing yet)    │
└──────────┬──────────┘
           │
    root.Get("user")
           │
           ▼
┌─────────────────────┐
│ Lazy Parse Path     │ ← 只解析访问的路径
│ Only parse "user"   │
└──────────┬──────────┘
           │
  .Get("name")
           │
           ▼
┌─────────────────────┐
│ Parse Leaf Value    │ ← 解析叶子节点
│ Only parse "name"   │
└──────────┬──────────┘
           │
           ▼
   "Alice" (string)

注意: 其他字段保持未解析状态
```

## 4. 内存分配对比

### encoding/json 内存分配
```
每次 Marshal 调用:

encodeState {           ← 从池获取，但需要重置
    Buffer [...]        ← 动态增长
    ptrSeen map         ← 循环引用检测
}
    ↓
string escape:
    需要转义时分配新缓冲区
    ↓
最终结果:
    buf := append([]byte(nil), e.Bytes()...)  ← 拷贝数据
    ↓
放回池:
    encodeStatePool.Put(e)

总分配: 
• encodeState: 1 alloc (复用)
• Buffer 扩容: 0-N allocs
• 转义临时缓冲: 0-M allocs  
• 最终结果拷贝: 1 alloc
• 总计: ~2-10 allocs
```

### Sonic 内存分配
```
每次 Marshal 调用:

encoder {              ← 从池获取
    buf [8192]byte     ← 预分配大缓冲
    compiled *func     ← 编译好的函数
}
    ↓
直接写入缓冲区:
    无需临时分配
    SIMD 批量写入
    ↓
返回结果:
    return buf[:pos]   ← 直接返回切片，无拷贝
    ↓
延迟放回池:
    (通过 GC 触发或显式调用)

总分配:
• encoder: 1 alloc (复用)
• Buffer 扩容: 0-1 allocs (预分配足够大)
• 无转义临时缓冲
• 无最终拷贝
• 总计: ~1-2 allocs (减少 50-70%)
```

## 5. 字符串处理对比

### encoding/json 字符串转义
```
Input: "Hello \"World\"\n"
       [H][e][l][l][o][ ]["][W][o][r][l][d]["][\\][n]

Processing (逐字节):

i=0: 'H' → safe → write 'H'
i=1: 'e' → safe → write 'e'  
i=2: 'l' → safe → write 'l'
i=3: 'l' → safe → write 'l'
i=4: 'o' → safe → write 'o'
i=5: ' ' → safe → write ' '
i=6: '"' → ESCAPE → write '\\"'
i=7: 'W' → safe → write 'W'
...
每个字节单独处理，分支密集
```

### Sonic 字符串转义 (SIMD)
```
Input: "Hello \"World\"\n"
       [H][e][l][l][o][ ]["][W][o][r][l][d]["][\\][n]

Processing (32字节/次):

Step 1: Load 32 bytes into YMM0
  YMM0 = [H][e][l][l][o][ ]["][W][o][r][l][d]["][\\][n]...

Step 2: Parallel compare (VPCMPGTB)
  Compare all 32 bytes against escape chars simultaneously
  Result: [0][0][0][0][0][0][1][0][0][0][0][0][1][1][0]...
                            ↑              ↑ ↑
                          '"'            '"' '\n'

Step 3: Generate mask (VPMOVMSKB)
  Mask = 0x00006040 (位表示需要转义的位置)

Step 4: Process escapes
  如果 mask == 0: 直接写入32字节 (fast path)
  否则: 只处理需要转义的字节

效率: 一次处理32字节 vs 逐字节，快 2-3倍
```

## 6. 类型处理对比图

### encoding/json 类型处理
```
type User struct {
    ID   int64
    Name string
}

每次 Marshal:
    ↓
┌──────────────────────────────┐
│ rt := reflect.TypeOf(user)  │ ← 反射获取类型
└────────────┬─────────────────┘
             │
             ▼
┌──────────────────────────────┐
│ encoder := typeEncoder(rt)   │ ← 查找编码器
│ • Cache lookup in sync.Map   │
│ • If miss, create encoder    │
└────────────┬─────────────────┘
             │
             ▼
┌──────────────────────────────┐
│ encoder(e, v, opts)          │ ← 接口调用
└────────────┬─────────────────┘
             │
             ▼
┌──────────────────────────────┐
│ structEncoder.encode()       │
│ • for i := 0; i < numField   │
│ • f := v.Field(i)            │ ← 反射每个字段
│ • f.Type().Kind()            │ ← 类型判断
│ • encode(f)                  │ ← 递归编码
└──────────────────────────────┘

开销: 反射 + 接口调用 + 动态分派
```

### Sonic 类型处理 (JIT)
```
type User struct {
    ID   int64
    Name string
}

首次 Marshal:
    ↓
┌──────────────────────────────┐
│ compiled := cache[User]      │ ← 查找缓存
│ if compiled == nil {         │
│   compiled = jit.Compile()   │ ← JIT编译
│   cache[User] = compiled     │
│ }                            │
└────────────┬─────────────────┘
             │
             ▼
    JIT 编译生成的伪代码:
    
    func marshal_User(u *User, buf *[]byte) {
        writeByte(buf, '{')
        
        // ID 字段 - 直接内存访问
        writeString(buf, `"id":`)
        writeInt64(buf, *(*int64)(unsafe.Pointer(
            uintptr(unsafe.Pointer(u)) + 0))  // offset 0
        )
        
        writeByte(buf, ',')
        
        // Name 字段 - 直接内存访问  
        writeString(buf, `"name":"`)
        str := *(*string)(unsafe.Pointer(
            uintptr(unsafe.Pointer(u)) + 8))  // offset 8
        writeStringEscapedSIMD(buf, str)
        writeByte(buf, '"')
        
        writeByte(buf, '}')
    }

后续 Marshal:
    ↓
┌──────────────────────────────┐
│ compiled := cache[User]      │ ← 命中缓存
│ compiled.Call(user, buf)     │ ← 直接调用
└──────────────────────────────┘

开销: 几乎为零（直接调用机器码）
```

## 7. 并发性能对比

### encoding/json 并发模型
```
Goroutine 1         Goroutine 2         Goroutine 3
     │                   │                   │
     ▼                   ▼                   ▼
 json.Marshal       json.Marshal       json.Marshal
     │                   │                   │
     ▼                   ▼                   ▼
encodeStatePool    encodeStatePool    encodeStatePool
 Get() → e1         Get() → e2         Get() → e3
     │                   │                   │
     ▼                   ▼                   ▼
 encode(user)       encode(user)       encode(user)
 • Reflection       • Reflection       • Reflection
 • Type check       • Type check       • Type check
 • Field loop       • Field loop       • Field loop
     │                   │                   │
     ▼                   ▼                   ▼
 buf := copy(e1)    buf := copy(e2)    buf := copy(e3)
     │                   │                   │
     ▼                   ▼                   ▼
 Put(e1)            Put(e2)            Put(e3)
     │                   │                   │
     ▼                   ▼                   ▼
   result1            result2            result3

特点:
• 每个协程独立反射
• 每次都要类型检查
• 最终结果需要拷贝
• sync.Map 查找开销
```

### Sonic 并发模型
```
Goroutine 1         Goroutine 2         Goroutine 3
     │                   │                   │
     ▼                   ▼                   ▼
 sonic.Marshal      sonic.Marshal      sonic.Marshal
     │                   │                   │
     ▼                   ▼                   ▼
 compiledCode[User] compiledCode[User] compiledCode[User]
     ↓                   ↓                   ↓
  (共享编译好的代码，只读，无锁)
     │                   │                   │
     ▼                   ▼                   ▼
  encoderPool        encoderPool        encoderPool
  Get() → enc1       Get() → enc2       Get() → enc3
     │                   │                   │
     ▼                   ▼                   ▼
  compiled.Call()    compiled.Call()    compiled.Call()
  • No reflection    • No reflection    • No reflection
  • Direct access    • Direct access    • Direct access
  • SIMD ops         • SIMD ops         • SIMD ops
     │                   │                   │
     ▼                   ▼                   ▼
  return enc1.buf    return enc2.buf    return enc3.buf
  (No copy)          (No copy)          (No copy)
     │                   │                   │
     ▼                   ▼                   ▼
  Put(enc1)          Put(enc2)          Put(enc3)
     │                   │                   │
     ▼                   ▼                   ▼
   result1            result2            result3

特点:
• 共享编译代码（无锁）
• 无需反射
• 无需拷贝
• 更好的缓存局部性
• 2-3x 并发性能提升
```

## 总结

这些架构图展示了 Sonic 和 encoding/json 在以下方面的核心差异：

1. **类型处理**: 反射 vs JIT 编译
2. **字符串处理**: 逐字节 vs SIMD 批量
3. **内存管理**: 频繁分配拷贝 vs 池化零拷贝
4. **解析策略**: 完整解析 vs 懒加载
5. **并发模型**: 重复工作 vs 共享编译代码

Sonic 通过这些优化实现了 3-4 倍的性能提升，但代价是平台限制、使用 unsafe、调试困难等。
