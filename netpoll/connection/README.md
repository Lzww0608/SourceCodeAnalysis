# Connection - Netpoll 的核心抽象层

Connection 是 Netpoll 中最核心的抽象，它将底层的文件描述符（FD）、事件驱动的 Poll 机制和零拷贝的 LinkBuffer 完美地结合在一起，为上层应用提供了高性能且易用的网络 I/O 接口。

## 🎯 核心定位

Connection 的设计目标是：
1. **零拷贝 I/O**：直接操作内核态和用户态之间的缓冲区，避免数据复制
2. **事件驱动**：基于 epoll/kqueue 的非阻塞 I/O 模型
3. **协议无关**：提供通用的 Reader/Writer 接口，适配各种应用层协议
4. **生命周期管理**：完整的连接状态机和回调机制

---

## 📐 架构设计

### 核心接口定义

```go
// Connection supports reading and writing simultaneously,
// but does not support simultaneous reading or writing by multiple goroutines.
type Connection interface {
    net.Conn  // 兼容标准库接口
    
    // 零拷贝读写接口
    Reader() Reader
    Writer() Writer
    
    // 连接状态
    IsActive() bool
    
    // 超时控制
    SetReadTimeout(timeout time.Duration) error
    SetWriteTimeout(timeout time.Duration) error
    SetIdleTimeout(timeout time.Duration) error
    
    // 回调设置
    SetOnRequest(on OnRequest) error
    AddCloseCallback(callback CloseCallback) error
}
```

### 实现结构体

```go
type connection struct {
    netFD         // 封装底层的文件描述符
    onEvent       // 事件回调管理
    locker        // 并发控制
    
    // 核心组件
    operator      *FDOperator     // FD 操作器，连接 Poll 和 Connection
    
    // I/O 缓冲区
    inputBuffer   *LinkBuffer     // 输入缓冲区（读）
    outputBuffer  *LinkBuffer     // 输出缓冲区（写）
    outputBarrier *barrier        // 写入屏障，用于同步
    
    // 超时控制
    readTimeout   time.Duration
    readDeadline  int64
    readTimer     *time.Timer
    readTrigger   chan error
    waitReadSize  int64
    
    writeTimeout  time.Duration
    writeDeadline int64
    writeTimer    *time.Timer
    writeTrigger  chan error
    
    // 缓冲区管理
    maxSize       int             // 两次 Release() 之间的最大数据量
    bookSize      int             // 单次读取的预期大小
    
    // 连接状态
    state         connState       // 连接状态：None/Connected/Disconnected
}
```

---

## 🔗 Connection 与 LinkBuffer 的结合

### 1. 输入流程（Read）

Connection 将底层的 socket 数据读入 `inputBuffer`，上层应用通过 Connection 的 Reader 接口进行零拷贝读取。

```
┌─────────────┐
│   Socket    │
│     FD      │
└──────┬──────┘
       │ epoll 事件触发
       ▼
┌─────────────┐
│ FDOperator  │ ◄─── inputs() 提供缓冲区
│   .inputs() │      inputAck(n) 确认读取了 n 字节
│ .inputAck() │
└──────┬──────┘
       │
       ▼
┌─────────────┐
│ inputBuffer │ ◄─── LinkBuffer
│ (LinkBuffer)│      book() 预订空间
└──────┬──────┘      bookAck(n) 提交数据
       │
       ▼
┌─────────────┐
│ Application │
│  Reader API │ ◄─── Next/Peek/Skip 零拷贝读取
└─────────────┘
```

#### 核心方法：inputs & inputAck

```go
// inputs 实现 FDOperator 接口
// 当 epoll 检测到 FD 可读时，Poll 调用此方法获取可写入的缓冲区
func (c *connection) inputs(vs [][]byte) (rs [][]byte) {
    // 使用 book 方法预订内存
    // bookSize: 期望读取的大小（动态调整）
    // maxSize: 最大允许的大小
    vs[0] = c.inputBuffer.book(c.bookSize, c.maxSize)
    return vs[:1]
}

// inputAck 实现 FDOperator 接口
// 当 Poll 实际从 socket 读取了 n 字节后，调用此方法确认
func (c *connection) inputAck(n int) (err error) {
    if n <= 0 {
        c.inputBuffer.bookAck(0)
        return nil
    }
    
    // 自适应调整 bookSize（指数增长，上限 mallocMax）
    if n == c.bookSize && c.bookSize < mallocMax {
        c.bookSize <<= 1  // 翻倍
    }
    
    // 提交数据到 inputBuffer
    length, _ := c.inputBuffer.bookAck(n)
    
    // 更新 maxSize
    if c.maxSize < length {
        c.maxSize = length
    }
    if c.maxSize > mallocMax {
        c.maxSize = mallocMax
    }
    
    // 触发 onRequest 回调或唤醒等待的 Reader
    needTrigger := true
    if length == n {  // 首次接收数据
        needTrigger = c.onRequest()
    }
    if needTrigger && length >= int(atomic.LoadInt64(&c.waitReadSize)) {
        c.triggerRead(nil)
    }
    return nil
}
```

**关键设计点：**
1. **book/bookAck 模式**：先预订空间，再确认实际使用量，避免浪费
2. **自适应 bookSize**：根据实际读取量动态调整预订大小，优化性能
3. **零拷贝保证**：`inputBuffer.book()` 返回的切片直接指向 LinkBuffer 内部，Poll 直接写入，无需中间拷贝

### 2. 输出流程（Write）

应用通过 Connection 的 Writer 接口写入 `outputBuffer`，数据最终通过 Poll 异步发送到 socket。

```
┌─────────────┐
│ Application │
│  Writer API │ ──── Malloc/WriteBinary/Flush
└──────┬──────┘
       │
       ▼
┌─────────────┐
│outputBuffer │ ◄─── LinkBuffer
│ (LinkBuffer)│      Malloc() 分配空间
└──────┬──────┘      Flush() 提交数据
       │
       ▼
┌─────────────┐
│ FDOperator  │ ◄─── outputs() 获取待发送数据
│  .outputs() │      outputAck(n) 确认发送了 n 字节
│.outputAck() │
└──────┬──────┘
       │ epoll 监听 POLLOUT
       ▼
┌─────────────┐
│   Socket    │
│     FD      │
└─────────────┘
```

#### 核心方法：outputs & outputAck

```go
// outputs 实现 FDOperator 接口
// 当 epoll 检测到 FD 可写时，Poll 调用此方法获取待发送的数据
func (c *connection) outputs(vs [][]byte) (rs [][]byte, _ bool) {
    if c.outputBuffer.IsEmpty() {
        c.rw2r()  // 切换为只监听读事件
        return rs, false
    }
    // 获取所有待发送的数据块（向量化 I/O）
    rs = c.outputBuffer.GetBytes(vs)
    return rs, false
}

// outputAck 实现 FDOperator 接口
// 当 Poll 实际发送了 n 字节后，调用此方法确认
func (c *connection) outputAck(n int) (err error) {
    if n > 0 {
        c.outputBuffer.Skip(n)   // 跳过已发送的数据
        c.outputBuffer.Release() // 释放已读节点
    }
    if c.outputBuffer.IsEmpty() {
        c.rw2r()  // 无数据可发送，停止监听写事件
    }
    return nil
}
```

**关键设计点：**
1. **GetBytes 向量化**：返回多个不连续的内存块，配合 `writev` 系统调用实现零拷贝
2. **事件切换**：数据发送完毕后自动从 `POLLIN|POLLOUT` 切换为 `POLLIN`，减少无意义的唤醒
3. **异步发送**：`Flush()` 只是将数据提交到 `outputBuffer`，实际发送由 Poll 异步完成

---

## ⚙️ Connection 与 FDOperator 的结合

### FDOperator：连接 Poll 和 Connection 的桥梁

`FDOperator` 是一个关键的中间层，它将文件描述符和 Connection 的操作绑定在一起。

```go
type FDOperator struct {
    FD int  // 文件描述符
    
    // 事件回调（由 Poll 调用）
    OnRead  func(p Poll) error
    OnWrite func(p Poll) error
    OnHup   func(p Poll) error
    
    // 数据传输接口（由 Poll 调用）
    Inputs    func(vs [][]byte) (rs [][]byte)
    InputAck  func(n int) (err error)
    Outputs   func(vs [][]byte) (rs [][]byte, supportZeroCopy bool)
    OutputAck func(n int) (err error)
    
    poll Poll  // 注册的 Poll 实例
    
    detached int32  // 是否已从 Poll 中分离
    state    int32  // 状态：0(unused) 1(inuse) 2(do-done)
}
```

### Connection 如何设置 FDOperator

在 Connection 初始化时，会创建并配置 FDOperator：

```go
func newConnection(fd int) *connection {
    c := &connection{
        netFD:        netFD{fd: fd},
        inputBuffer:  NewLinkBuffer(),
        outputBuffer: NewLinkBuffer(),
        // ...
    }
    
    // 创建 FDOperator
    c.operator = &FDOperator{
        FD: fd,
        // 绑定回调
        Inputs:    c.inputs,
        InputAck:  c.inputAck,
        Outputs:   c.outputs,
        OutputAck: c.outputAck,
    }
    
    return c
}
```

### 事件处理流程

当 epoll 检测到事件时，Poll 会调用 FDOperator 的回调：

```
epoll_wait() 返回事件
        │
        ▼
┌─────────────────┐
│  Poll.handler() │
└────────┬────────┘
         │
         ├─── 读事件 ──► operator.OnRead(p) ──► operator.Inputs()
         │                                      operator.InputAck(n)
         │
         ├─── 写事件 ──► operator.OnWrite(p) ─► operator.Outputs()
         │                                      operator.OutputAck(n)
         │
         └─── 挂断 ────► operator.OnHup(p) ───► connection.onHup()
```

---

## 🔄 事件处理与回调机制

### 连接状态机

```go
const (
    connStateNone         = 0  // 初始状态
    connStateConnected    = 1  // 已连接
    connStateDisconnected = 2  // 已断开
)
```

### 回调类型

1. **OnPrepare**：连接准备阶段（注册到 Poll 之前）
2. **OnConnect**：连接建立后（可用于认证、初始化）
3. **OnRequest**：有数据可读时
4. **OnDisconnect**：连接断开时
5. **CloseCallback**：连接关闭后（可注册多个）

### 事件处理流程

```go
type onEvent struct {
    ctx                  context.Context
    onConnectCallback    atomic.Value  // OnConnect
    onDisconnectCallback atomic.Value  // OnDisconnect  
    onRequestCallback    atomic.Value  // OnRequest
    closeCallbacks       atomic.Value  // CloseCallback 链表
}
```

#### OnConnect 流程

```go
func (c *connection) onConnect() {
    onConnect, _ := c.onConnectCallback.Load().(OnConnect)
    if onConnect == nil {
        c.changeState(connStateNone, connStateConnected)
        return
    }
    
    if !c.lock(connecting) {
        return
    }
    
    onRequest, _ := c.onRequestCallback.Load().(OnRequest)
    c.onProcess(onConnect, onRequest)
}
```

**关键点**：
- OnConnect 只执行一次（通过状态机保证）
- 执行期间持有 `connecting` 锁
- 执行完后会检查是否有数据需要触发 OnRequest

#### OnRequest 流程

```go
func (c *connection) onRequest() (needTrigger bool) {
    onRequest, ok := c.onRequestCallback.Load().(OnRequest)
    if !ok {
        return true
    }
    
    // 等待 OnConnect 完成
    if c.getState() == connStateNone && c.onConnectCallback.Load() != nil {
        return  // 让 OnConnect 调用 OnRequest
    }
    
    processed := c.onProcess(nil, onRequest)
    return !processed
}
```

**关键点**：
- OnRequest 可能被多次调用（每次有新数据时）
- 必须等待 OnConnect 完成
- 通过 `processing` 锁保证串行执行

#### onProcess：核心处理逻辑

这是 Netpoll 中最复杂也是最精妙的部分，它保证了：
1. OnConnect 和 OnRequest 的串行执行
2. 循环处理所有可读数据
3. 正确处理连接关闭
4. Panic 恢复

```go
func (c *connection) onProcess(onConnect OnConnect, onRequest OnRequest) (processed bool) {
    // 获取 processing 锁（只有一个 goroutine 可以处理）
    if !c.lock(processing) {
        return false
    }
    
    task := func() {
        panicked := true
        defer func() {
            if !panicked {
                return
            }
            // Panic 恢复：解锁并关闭连接
            c.unlock(processing)
            if c.IsActive() {
                c.Close()
            } else {
                c.closeCallback(false, false)
            }
        }()
        
        // 1. 执行 OnConnect（如果存在）
        if onConnect != nil && c.changeState(connStateNone, connStateConnected) {
            c.ctx = onConnect(c.ctx, c)
            
            // 如果 OnConnect 中关闭了连接，触发 OnDisconnect
            if !c.IsActive() && c.changeState(connStateConnected, connStateDisconnected) {
                onDisconnect, _ := c.onDisconnectCallback.Load().(OnDisconnect)
                if onDisconnect != nil {
                    onDisconnect(c.ctx, c)
                }
            }
            c.unlock(connecting)
        }
        
    START:
        // 2. 执行 OnRequest（至少一次，如果有数据）
        if onRequest != nil && c.Reader().Len() > 0 {
            _ = onRequest(c.ctx, c)
        }
        
        // 3. 循环处理数据
        var closedBy who
        for {
            closedBy = c.status(closing)
            // 退出条件：用户关闭 / 无回调 / 无数据
            if closedBy == user || onRequest == nil || c.Reader().Len() == 0 {
                break
            }
            _ = onRequest(c.ctx, c)
        }
        
        // 4. 处理关闭回调
        if closedBy != none {
            needDetach := closedBy == user
            c.closeCallback(false, needDetach)
            panicked = false
            return
        }
        
        c.unlock(processing)
        
        // 5. 双重检查（避免竞态）
        if c.status(closing) != 0 && c.lock(processing) {
            c.closeCallback(false, false)
            panicked = false
            return
        }
        
        // 6. 检查是否有新数据到达
        if onRequest != nil && c.Reader().Len() > 0 && c.lock(processing) {
            goto START  // 重新处理
        }
        
        panicked = false
    }
    
    // 提交任务到协程池
    runner.RunTask(c.ctx, task)
    return true
}
```

**关键设计点**：

1. **串行保证**：`processing` 锁确保同一时刻只有一个 goroutine 在执行 OnRequest
2. **循环处理**：持续调用 OnRequest 直到数据被消费完或连接关闭
3. **Panic 安全**：defer + recover 机制，保证 Panic 不会导致连接泄漏
4. **双重检查**：解锁后再次检查状态，避免与 Poller 的竞态条件
5. **goto START**：检测到新数据时重新进入处理循环

---

## 🔒 并发控制：locker

Connection 使用位标志（bit flags）实现细粒度的并发控制：

```go
type locker struct {
    // 0 1 2 3 4 ..... 64
    // └─────┬──────┘
    //    lock bits
    state int64
}

const (
    closing    who = 0x01  // 连接正在关闭
    flushing   who = 0x02  // 正在刷新输出缓冲区
    connecting who = 0x04  // 正在执行 OnConnect
    processing who = 0x08  // 正在执行 OnRequest
    user       who = 0x10  // 用户触发的关闭
    poller     who = 0x20  // Poller 触发的关闭
)
```

### 核心方法

```go
// lock 尝试获取指定的锁
func (l *locker) lock(w who) (success bool) {
    return atomic.CompareAndSwapInt64(&l.state, 
        atomic.LoadInt64(&l.state) & ^int64(w),  // expected (没有该位)
        atomic.LoadInt64(&l.state) | int64(w))   // new (设置该位)
}

// unlock 释放指定的锁
func (l *locker) unlock(w who) {
    atomic.StoreInt64(&l.state, atomic.LoadInt64(&l.state) & ^int64(w))
}

// isUnlock 检查是否未持有指定的锁
func (l *locker) isUnlock(w who) bool {
    return atomic.LoadInt64(&l.state) & int64(w) == 0
}
```

### 使用场景

1. **flushing**：保证 `Write/Flush` 操作不并发
   ```go
   func (c *connection) Flush() error {
       if !c.lock(flushing) {
           return Exception(ErrConcurrentAccess, "when flush")
       }
       defer c.unlock(flushing)
       // ...
   }
   ```

2. **processing**：保证 OnRequest 串行执行
3. **connecting**：保证 OnConnect 只执行一次
4. **closing**：标记连接关闭状态

---

## 🛡️ 生命周期管理

### 连接关闭流程

Connection 的关闭可能由两方触发：
1. **用户主动关闭**：调用 `conn.Close()`
2. **Poller 检测到挂断**：`OnHup` 回调

```
                    ┌──────────────┐
                    │  Connection  │
                    │   IsActive   │
                    └───────┬──────┘
                            │
              ┌─────────────┼─────────────┐
              │                           │
              ▼                           ▼
     ┌────────────────┐         ┌────────────────┐
     │ User: Close()  │         │ Poller: OnHup()│
     └────────┬───────┘         └────────┬───────┘
              │                           │
              ├──► closeBy(user)          ├──► closeBy(poller)
              │                           │
              ▼                           ▼
     ┌────────────────┐         ┌────────────────┐
     │  onClose()     │         │  onHup()       │
     └────────┬───────┘         └────────┬───────┘
              │                           │
              └──────────┬────────────────┘
                         │
                         ▼
              ┌──────────────────┐
              │  closeCallback() │
              │  ├─ Detach Operator
              │  ├─ Run CloseCallbacks
              │  └─ Close Buffers
              └──────────────────┘
```

### onClose（用户关闭）

```go
func (c *connection) onClose() error {
    // 尝试标记为 user 关闭
    if c.closeBy(user) {
        c.triggerRead(Exception(ErrConnClosed, "self close"))
        c.triggerWrite(Exception(ErrConnClosed, "self close"))
        // 需要主动 Detach
        c.closeCallback(true, true)
        return nil
    }
    
    // 已被 Poller 关闭，修改状态为 user
    c.force(closing, user)
    // Poller 已 Detach，不需要再次 Detach
    return c.closeCallback(true, false)
}
```

### onHup（Poller 关闭）

```go
func (c *connection) onHup(p Poll) error {
    if !c.closeBy(poller) {
        return nil
    }
    
    c.triggerRead(Exception(ErrEOF, "peer close"))
    c.triggerWrite(Exception(ErrConnClosed, "peer close"))
    
    // 调用 OnDisconnect
    c.onDisconnect()
    
    // 如果没有设置回调，由用户负责关闭
    onConnect := c.onConnectCallback.Load()
    onRequest := c.onRequestCallback.Load()
    needCloseByUser := onConnect == nil && onRequest == nil
    if !needCloseByUser {
        // Poller 已 Detach，不需要再次 Detach
        c.closeCallback(true, false)
    }
    return nil
}
```

### closeCallback

```go
func (c *connection) closeCallback(needLock, needDetach bool) (err error) {
    // 获取 processing 锁（如果需要）
    if needLock && !c.lock(processing) {
        return nil
    }
    
    // 从 Poll 中分离（如果需要）
    if needDetach && c.operator.poll != nil {
        if err := c.operator.Control(PollDetach); err != nil {
            logger.Printf("NETPOLL: closeCallback detach operator failed: %v", err)
        }
    }
    
    // 执行所有 CloseCallback（逆序）
    latest := c.closeCallbacks.Load()
    if latest == nil {
        return nil
    }
    for callback := latest.(*callbackNode); callback != nil; callback = callback.pre {
        callback.fn(c)
    }
    
    // 关闭缓冲区
    c.closeBuffer()
    
    return nil
}
```

**关键点**：
1. **Detach 时机**：用户关闭需要 Detach，Poller 触发的关闭已经 Detach
2. **回调执行**：逆序执行（LIFO），最后注册的最先执行
3. **缓冲区清理**：关闭 inputBuffer 和 outputBuffer，归还到对象池

---

## 🎓 最佳实践与注意事项

### 1. Reader/Writer 不支持并发

Connection 的 `Reader()` 和 `Writer()` **不是线程安全的**：

```go
// ❌ 错误示例
go func() {
    data, _ := conn.Reader().Next(10)
    // ...
}()
go func() {
    data, _ := conn.Reader().Next(10)  // 竞态！
    // ...
}()
```

**正确做法**：在 OnRequest 中串行处理数据。

### 2. 必须消费数据或关闭连接

OnRequest 必须满足以下条件之一，否则会死循环：
1. 读取所有可读数据（`Reader().Len() == 0`）
2. 主动关闭连接（`conn.Close()`）

```go
// ❌ 错误示例
func OnRequest(ctx context.Context, conn Connection) error {
    // 只读了一部分数据
    conn.Reader().Next(10)
    return nil  // 还有数据未读，会立即再次调用 OnRequest
}

// ✅ 正确示例
func OnRequest(ctx context.Context, conn Connection) error {
    for conn.Reader().Len() > 0 {
        // 处理数据
        processData(conn.Reader())
    }
    return nil
}
```

### 3. Release 的时机

调用 `Reader().Next()` 后的数据会一直占用内存，直到调用 `Release()`：

```go
// ✅ 推荐做法
func OnRequest(ctx context.Context, conn Connection) error {
    defer conn.Reader().Release()  // 确保释放
    
    for conn.Reader().Len() > 0 {
        data, _ := conn.Reader().Next(100)
        process(data)
    }
    return nil
}
```

### 4. Flush 的时机

`Malloc()` 分配的内存对 Reader 不可见，直到调用 `Flush()`：

```go
buf, _ := conn.Writer().Malloc(100)
copy(buf, data)
conn.Writer().Flush()  // 必须调用 Flush
```

---

## 📊 性能优化技巧

### 1. 自适应的 bookSize

Connection 会根据实际读取量动态调整 `bookSize`（初始 4KB，最大 8MB）：

- 如果每次都读满，说明数据量大，下次翻倍
- 如果长时间不读满，会重置为较小值

### 2. maxSize 控制

`maxSize` 限制两次 `Release()` 之间的最大数据量，防止内存膨胀。

### 3. 向量化 I/O

`outputBuffer.GetBytes()` 返回多个不连续的内存块，配合 `writev` 系统调用实现零拷贝发送。

---

## 🔍 总结

Connection 是 Netpoll 的核心，它完美地将以下组件结合在一起：

1. **LinkBuffer**：零拷贝的内存管理
2. **FDOperator**：事件驱动的 I/O 抽象
3. **Poll**：高性能的事件循环
4. **回调机制**：灵活的生命周期管理

通过精妙的设计，Connection 实现了：
- **高性能**：零拷贝 + 事件驱动 + 对象池
- **易用性**：类 net.Conn 的 API，支持多种读写模式
- **可靠性**：完善的状态机、并发控制和错误处理

理解 Connection 的实现细节，是掌握 Netpoll 乃至构建高性能网络服务的关键。

