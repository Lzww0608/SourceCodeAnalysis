# Connection 的 Reader/Writer 实现详解

Connection 实现了 `Reader` 和 `Writer` 接口，为上层应用提供了零拷贝的读写能力。本文深入分析这两个接口的实现细节。

## Reader 接口实现

Connection 通过 `inputBuffer` 实现 Reader 接口，所有读取操作最终都委托给 LinkBuffer。

### 核心读取方法

#### 1. Next - 消费性读取

```go
func (c *connection) Next(n int) (p []byte, err error) {
    // 等待至少 n 字节可读
    if err = c.waitRead(n); err != nil {
        return p, err
    }
    // 委托给 inputBuffer
    return c.inputBuffer.Next(n)
}
```

**特点**：
- **阻塞等待**：如果数据不足，会阻塞直到超时或数据到达
- **移动读指针**：调用后 `Reader().Len()` 会减少 n
- **零拷贝**：如果数据在单个节点内，返回的是直接引用

#### 2. Peek - 预览性读取

```go
func (c *connection) Peek(n int) (buf []byte, err error) {
    if err = c.waitRead(n); err != nil {
        return buf, err
    }
    // Peek 不移动读指针
    return c.inputBuffer.Peek(n)
}
```

**使用场景**：
- 判断协议魔数（Magic Number）
- 读取包头确定包体长度
- 预检查数据格式

**示例**：
```go
// 先 Peek 协议头
header, _ := conn.Reader().Peek(4)
if !isValidMagic(header) {
    conn.Close()
    return
}
// 再读取完整数据包
packetLen := parseLength(header)
packet, _ := conn.Reader().Next(packetLen)
```

#### 3. Skip - 跳过数据

```go
func (c *connection) Skip(n int) (err error) {
    if err = c.waitRead(n); err != nil {
        return err
    }
    return c.inputBuffer.Skip(n)
}
```

**特点**：
- 比 `Next` 更高效（不需要返回数据）
- 用于跳过无用的填充字节或已知的固定头部

#### 4. Until - 按分隔符读取

```go
func (c *connection) Until(delim byte) (line []byte, err error) {
    var n, l int
    for {
        // 等待至少 n+1 字节（增量等待）
        if err = c.waitRead(n + 1); err != nil {
            // 超时或错误，返回所有数据
            line, _ = c.inputBuffer.Next(c.inputBuffer.Len())
            return
        }
        
        l = c.inputBuffer.Len()
        // 在 inputBuffer 中搜索分隔符
        i := c.inputBuffer.indexByte(delim, n)
        if i < 0 {
            n = l  // 跳过已搜索的部分
            continue
        }
        // 找到分隔符，读取包含分隔符的数据
        return c.Next(i + 1)
    }
}
```

**使用场景**：
- 读取文本协议（如 HTTP、Redis RESP）
- 按行读取日志

**示例**：
```go
// 读取一行（以 \n 结尾）
line, err := conn.Reader().Until('\n')
if err != nil {
    // 超时或连接关闭
    return
}
// line 包含 \n
content := line[:len(line)-1]
```

#### 5. ReadString / ReadBinary

```go
// ReadString 返回字符串（会拷贝）
func (c *connection) ReadString(n int) (s string, err error) {
    if err = c.waitRead(n); err != nil {
        return s, err
    }
    return c.inputBuffer.ReadString(n)
}

// ReadBinary 返回 []byte 拷贝（安全的）
func (c *connection) ReadBinary(n int) (p []byte, err error) {
    if err = c.waitRead(n); err != nil {
        return p, err
    }
    return c.inputBuffer.ReadBinary(n)
}
```

**对比 Next**：
- `Next`：可能零拷贝，但生命周期与 LinkBuffer 绑定
- `ReadString/ReadBinary`：总是拷贝，数据独立，更安全

**选择建议**：
- 数据需要长期保存 → 使用 `ReadBinary`
- 数据立即处理完毕 → 使用 `Next`（更高效）

#### 6. Release - 释放已读内存

```go
func (c *connection) Release() (err error) {
    // 优化：只有在 inputBuffer 为空时才处理 maxSize
    if c.inputBuffer.Len() == 0 && c.operator.do() {
        maxSize := c.inputBuffer.calcMaxSize()
        // 限制 maxSize 上限，防止 GC 压力
        if maxSize > mallocMax {
            maxSize = mallocMax
        }
        
        if maxSize > c.maxSize {
            c.maxSize = maxSize
        }
        
        // 重置 tail 节点（强制切断过大的节点）
        if c.inputBuffer.Len() == 0 {
            c.inputBuffer.resetTail(c.maxSize)
        }
        c.operator.done()
    }
    // 释放已读节点
    return c.inputBuffer.Release()
}
```

**关键优化**：
1. **calcMaxSize**：计算历史最大数据量，用于优化内存分配
2. **resetTail**：防止单个节点无限膨胀导致内存泄漏
3. **operator.do/done**：通过 CAS 锁避免与 Poller 的竞态

#### 7. Slice - 切片引用

```go
func (c *connection) Slice(n int) (r Reader, err error) {
    if err = c.waitRead(n); err != nil {
        return nil, err
    }
    // 返回一个新的只读 LinkBuffer
    return c.inputBuffer.Slice(n)
}
```

**使用场景**：
- 将数据传递给其他模块（如解析器）
- 实现零拷贝的数据转发

**注意**：
- 返回的 Reader 共享底层内存
- 原 Connection Release 之前，Slice 的数据都有效

### waitRead：阻塞等待机制

这是 Reader 实现的核心，负责在数据不足时阻塞等待：

```go
func (c *connection) waitRead(n int) (err error) {
    if n <= c.inputBuffer.Len() {
        return nil  // 数据已足够
    }
    
    // 设置期望的数据量
    atomic.StoreInt64(&c.waitReadSize, int64(n))
    defer atomic.StoreInt64(&c.waitReadSize, 0)
    
    // 检查连接状态
    if !c.IsActive() {
        return Exception(ErrConnClosed, "wait read")
    }
    
    // 计算超时时间
    var deadline time.Time
    if c.readDeadline != 0 {
        deadline = time.Unix(0, c.readDeadline)
    } else if c.readTimeout > 0 {
        deadline = time.Now().Add(c.readTimeout)
    }
    
    // 等待数据或超时
    if deadline.IsZero() {
        // 无超时，一直等待
        <-c.readTrigger
    } else {
        // 有超时，使用 Timer
        if c.readTimer == nil {
            c.readTimer = time.NewTimer(time.Until(deadline))
        } else {
            c.readTimer.Reset(time.Until(deadline))
        }
        select {
        case <-c.readTimer.C:
            return Exception(ErrReadTimeout, "wait read")
        case <-c.readTrigger:
        }
    }
    
    // 检查触发原因
    return <-c.readTrigger
}
```

**工作流程**：
1. **快速路径**：数据已足够，立即返回
2. **设置 waitReadSize**：告诉 Poller 需要多少数据
3. **阻塞等待**：通过 channel 阻塞当前 goroutine
4. **超时控制**：支持 Deadline 和 Timeout 两种模式

**唤醒机制**：
- Poller 读取到数据 → `inputAck()` → `triggerRead(nil)`
- 连接关闭 → `onHup()/onClose()` → `triggerRead(err)`

```go
func (c *connection) triggerRead(err error) {
    select {
    case c.readTrigger <- err:  // 非阻塞发送
    default:
    }
}
```

---

## Writer 接口实现

Connection 通过 `outputBuffer` 实现 Writer 接口，提供了多种写入模式。

### 核心写入方法

#### 1. Malloc / Flush - 零拷贝写入

```go
// 分配 n 字节的写入空间
func (c *connection) Malloc(n int) (buf []byte, err error) {
    return c.outputBuffer.Malloc(n)
}

// 提交写入的数据（触发发送）
func (c *connection) Flush() error {
    if !c.IsActive() {
        return Exception(ErrConnClosed, "when flush")
    }
    
    // 获取 flushing 锁（防止并发写入）
    if !c.lock(flushing) {
        return Exception(ErrConcurrentAccess, "when flush")
    }
    defer c.unlock(flushing)
    
    // 提交数据到 outputBuffer
    c.outputBuffer.Flush()
    // 实际发送
    return c.flush()
}
```

**使用示例**：
```go
// 零拷贝写入
buf, _ := conn.Writer().Malloc(100)
n := copy(buf, data)
conn.Writer().MallocAck(n)  // 如果没用满
conn.Writer().Flush()
```

**优势**：
- 避免了从用户缓冲区到 outputBuffer 的拷贝
- 适合序列化场景（直接序列化到 outputBuffer）

#### 2. WriteBinary / WriteString - 便捷写入

```go
func (c *connection) WriteBinary(b []byte) (n int, err error) {
    return c.outputBuffer.WriteBinary(b)
}

func (c *connection) WriteString(s string) (n int, err error) {
    return c.outputBuffer.WriteString(s)
}
```

**特点**：
- **小数据拷贝**：< 4KB 时拷贝到 outputBuffer
- **大数据引用**：>= 4KB 时直接引用（零拷贝）
- **不自动 Flush**：需要手动调用 `Flush()`

**使用建议**：
```go
// ✅ 批量写入后统一 Flush
conn.Writer().WriteBinary(packet1)
conn.Writer().WriteBinary(packet2)
conn.Writer().WriteBinary(packet3)
conn.Writer().Flush()

// ❌ 每次都 Flush（性能差）
conn.Writer().WriteBinary(packet1)
conn.Writer().Flush()
conn.Writer().WriteBinary(packet2)
conn.Writer().Flush()
```

#### 3. WriteDirect - 插入式写入

```go
func (c *connection) WriteDirect(p []byte, remainCap int) (err error) {
    return c.outputBuffer.WriteDirect(p, remainCap)
}
```

**使用场景**：
- 在已分配的 Buffer 中插入协议头
- 避免数据搬移

**示例**：
```go
// 1. 先 Malloc Body 空间
bodyBuf, _ := conn.Writer().Malloc(bodySize)
writeBody(bodyBuf)

// 2. 插入 Header（在 Body 之前）
header := buildHeader(bodySize)
conn.Writer().WriteDirect(header, bodySize)

// 3. Flush
conn.Writer().Flush()

// 最终发送顺序：header -> body
```

#### 4. Write（net.Conn 兼容）

```go
func (c *connection) Write(p []byte) (n int, err error) {
    if !c.IsActive() {
        return 0, Exception(ErrConnClosed, "when write")
    }
    
    if !c.lock(flushing) {
        return 0, Exception(ErrConcurrentAccess, "when write")
    }
    defer c.unlock(flushing)
    
    // 拷贝数据
    dst, _ := c.outputBuffer.Malloc(len(p))
    n = copy(dst, p)
    // 立即 Flush
    c.outputBuffer.Flush()
    err = c.flush()
    return n, err
}
```

**特点**：
- 兼容 `net.Conn` 接口
- **总是拷贝 + 立即 Flush**
- 性能低于 `Malloc/Flush` 模式

---

## flush：实际发送逻辑

`Flush()` 调用后，数据并不一定立即发送，而是根据 `outputBuffer` 状态决定：

```go
func (c *connection) flush() error {
    // 情况 1：outputBuffer 为空，直接返回
    if c.outputBuffer.IsEmpty() {
        return nil
    }
    
    // 情况 2：首次写入，尝试立即发送
    // 如果 outputBuffer 之前为空，说明 Poller 没有监听 POLLOUT
    // 我们尝试直接调用 syscall.Write
    if firstFlush {
        n, err := syscall.Write(c.fd, c.outputBuffer.GetBytes(nil)[0])
        if err == nil {
            c.outputBuffer.Skip(n)
            c.outputBuffer.Release()
            if c.outputBuffer.IsEmpty() {
                return nil  // 全部发送完毕
            }
        }
        // 发送不完整或出错，fallthrough
    }
    
    // 情况 3：注册 POLLOUT 事件，等待 Poller 异步发送
    // 从 POLLIN 切换到 POLLIN | POLLOUT
    return c.operator.Control(PollR2RW)
}
```

**关键设计**：
1. **优化小数据**：立即尝试发送，避免 epoll 唤醒开销
2. **处理阻塞**：如果发送缓冲区满，切换为异步模式
3. **事件切换**：`PollR2RW` 让 Poller 监听写事件

---

## 超时控制机制

### 三种超时模式

1. **readTimeout / writeTimeout**：相对超时（每次操作）
   ```go
   conn.SetReadTimeout(5 * time.Second)
   data, _ := conn.Reader().Next(100)  // 最多等待 5 秒
   ```

2. **readDeadline / writeDeadline**：绝对超时（一次性）
   ```go
   conn.SetReadDeadline(time.Now().Add(10 * time.Second))
   data1, _ := conn.Reader().Next(100)  // 共享同一个 Deadline
   data2, _ := conn.Reader().Next(100)
   ```

3. **Deadline 优先**：如果同时设置，Deadline 覆盖 Timeout

### 实现细节

```go
func (c *connection) SetReadTimeout(timeout time.Duration) error {
    if timeout >= 0 {
        c.readTimeout = timeout
    }
    c.readDeadline = 0  // 清空 Deadline
    return nil
}

func (c *connection) SetReadDeadline(t time.Time) error {
    if t.IsZero() {
        c.readDeadline = 0
    } else {
        c.readDeadline = t.UnixNano()
    }
    return nil
}
```

**计算超时时间**（在 waitRead 中）：
```go
var deadline time.Time
if c.readDeadline != 0 {
    deadline = time.Unix(0, c.readDeadline)  // 优先使用 Deadline
} else if c.readTimeout > 0 {
    deadline = time.Now().Add(c.readTimeout)  // 否则使用 Timeout
}
```

---

## 并发安全性

### 不支持的并发模式

```go
// ❌ 多个 goroutine 同时读
go func() { conn.Reader().Next(10) }()
go func() { conn.Reader().Next(10) }()

// ❌ 多个 goroutine 同时写
go func() { conn.Writer().Write(data1) }()
go func() { conn.Writer().Write(data2) }()
```

### flushing 锁的保护

```go
func (c *connection) Flush() error {
    if !c.lock(flushing) {
        return Exception(ErrConcurrentAccess, "when flush")
    }
    defer c.unlock(flushing)
    // ...
}
```

**保护范围**：
- `Write()` 方法
- `Flush()` 方法

**不保护**：
- `Malloc()` / `WriteBinary()` 等（假设上层串行调用）

---

## 性能优化建议

### 1. 批量操作

```go
// ✅ 好：批量写入，减少 Flush 次数
for _, msg := range messages {
    conn.Writer().WriteBinary(msg)
}
conn.Writer().Flush()

// ❌ 差：每次都 Flush
for _, msg := range messages {
    conn.Write(msg)  // 内部会 Flush
}
```

### 2. 零拷贝模式

```go
// ✅ 好：直接序列化到 outputBuffer
buf, _ := conn.Writer().Malloc(estimatedSize)
n := proto.MarshalTo(buf, message)
conn.Writer().MallocAck(n)
conn.Writer().Flush()

// ❌ 差：先序列化，再拷贝
data := proto.Marshal(message)
conn.Write(data)
```

### 3. 及时 Release

```go
// ✅ 好：读取后立即 Release
func OnRequest(ctx context.Context, conn Connection) error {
    defer conn.Reader().Release()
    
    for conn.Reader().Len() > 0 {
        processPacket(conn.Reader())
    }
    return nil
}

// ❌ 差：忘记 Release，内存泄漏
func OnRequest(ctx context.Context, conn Connection) error {
    data, _ := conn.Reader().Next(100)
    process(data)
    return nil  // inputBuffer 节点无法释放
}
```

---

## 总结

Connection 的 Reader/Writer 实现展现了 Netpoll 的核心设计思想：

1. **零拷贝优先**：通过 LinkBuffer 避免内存拷贝
2. **异步发送**：写入和实际发送分离，提高吞吐量
3. **灵活的 API**：既支持零拷贝模式，也支持兼容模式
4. **智能调度**：自适应的缓冲区大小，优化的事件切换

掌握这些实现细节，才能充分发挥 Netpoll 的性能优势。

