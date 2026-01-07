# Connection 生命周期管理详解

Connection 的生命周期管理是 Netpoll 中最复杂的部分之一，涉及资源分配、事件注册、数据传输、优雅关闭等多个阶段。正确理解生命周期对于避免资源泄漏和正确使用 Netpoll 至关重要。

## 🔄 完整生命周期流程

```
┌─────────────────┐
│   创建阶段      │  newConnection()
│   (Creation)    │
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│   准备阶段      │  onPrepare()
│  (Preparation)  │  - 设置回调
└────────┬────────┘  - 调用 OnPrepare
         │          - 注册到 Poll
         ▼
┌─────────────────┐
│   连接阶段      │  onConnect()
│  (Connection)   │  - 执行 OnConnect
└────────┬────────┘  - 初始化 context
         │
         ▼
┌─────────────────┐
│   工作阶段      │  onRequest()
│   (Working)     │  - 循环处理数据
└────────┬────────┘  - 读写操作
         │
         ▼
┌─────────────────┐
│   关闭阶段      │  onClose() / onHup()
│   (Closing)     │  - 触发关闭
└────────┬────────┘  - OnDisconnect
         │
         ▼
┌─────────────────┐
│   清理阶段      │  closeCallback()
│  (Cleanup)      │  - CloseCallback
└─────────────────┘  - 释放资源
```

---

## 1️⃣ 创建阶段（Creation）

### 服务端：Accept 新连接

```go
// Listener.Accept() 内部流程
func (l *listener) Accept() (net.Conn, error) {
    // 1. 调用 accept 系统调用
    nfd, sa, err := syscall.Accept(l.fd)
    if err != nil {
        return nil, err
    }
    
    // 2. 设置非阻塞
    syscall.SetNonblock(nfd, true)
    
    // 3. 创建 Connection
    conn := newConnection(nfd)
    conn.remoteAddr = sa
    
    // 4. 准备 Connection
    if err = conn.onPrepare(l.opts); err != nil {
        conn.Close()
        return nil, err
    }
    
    return conn, nil
}
```

### 客户端：Dial 建立连接

```go
// Dialer.DialConnection() 内部流程
func (d *dialer) DialConnection(network, address string, timeout time.Duration) (Connection, error) {
    // 1. 创建 socket
    fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
    if err != nil {
        return nil, err
    }
    
    // 2. 设置非阻塞
    syscall.SetNonblock(fd, true)
    
    // 3. 调用 connect（非阻塞）
    sa := parseAddr(address)
    err = syscall.Connect(fd, sa)
    if err != nil && err != syscall.EINPROGRESS {
        syscall.Close(fd)
        return nil, err
    }
    
    // 4. 等待连接完成（通过 epoll）
    // ...
    
    // 5. 创建 Connection
    conn := newConnection(fd)
    conn.remoteAddr = sa
    
    // 6. 准备 Connection
    if err = conn.onPrepare(d.opts); err != nil {
        conn.Close()
        return nil, err
    }
    
    return conn, nil
}
```

### newConnection：初始化结构体

```go
func newConnection(fd int) *connection {
    c := &connection{
        netFD: netFD{fd: fd},
        // 初始化缓冲区
        inputBuffer:   NewLinkBuffer(),
        outputBuffer:  NewLinkBuffer(),
        outputBarrier: barrierPool.Get().(*barrier),
        // 初始化参数
        bookSize: defaultLinkBufferSize,  // 4KB
        maxSize:  defaultLinkBufferSize,
        state:    connStateNone,
        // 初始化 channel
        readTrigger:  make(chan error, 1),
        writeTrigger: make(chan error, 1),
    }
    
    // 创建 FDOperator
    c.operator = &FDOperator{
        FD:        fd,
        Inputs:    c.inputs,
        InputAck:  c.inputAck,
        Outputs:   c.outputs,
        OutputAck: c.outputAck,
    }
    
    return c
}
```

**关键资源**：
1. **inputBuffer / outputBuffer**：从 LinkBuffer 池分配
2. **outputBarrier**：从 barrier 池分配
3. **readTrigger / writeTrigger**：创建 channel
4. **FDOperator**：绑定回调函数

---

## 2️⃣ 准备阶段（Preparation）

### onPrepare：设置回调并注册

```go
func (c *connection) onPrepare(opts *options) (err error) {
    if opts != nil {
        // 1. 设置回调
        c.SetOnConnect(opts.onConnect)
        c.SetOnDisconnect(opts.onDisconnect)
        c.SetOnRequest(opts.onRequest)
        
        // 2. 设置超时
        c.SetReadTimeout(opts.readTimeout)
        c.SetWriteTimeout(opts.writeTimeout)
        c.SetIdleTimeout(opts.idleTimeout)
        
        // 3. 调用 OnPrepare 回调
        if opts.onPrepare != nil {
            c.ctx = opts.onPrepare(c)
        }
    }
    
    // 4. 初始化 context
    if c.ctx == nil {
        c.ctx = context.Background()
    }
    
    // 5. 注册到 Poll
    if c.IsActive() {
        return c.register()
    }
    
    return nil
}
```

### register：注册到 Poll

```go
func (c *connection) register() (err error) {
    // 注册为 POLLIN 事件
    err = c.operator.Control(PollReadable)
    if err != nil {
        logger.Printf("NETPOLL: connection register failed: %v", err)
        c.Close()
        return Exception(ErrConnClosed, err.Error())
    }
    return nil
}
```

**Control(PollReadable) 内部流程**：
1. 调用 `poll.Control(op, PollReadable)`
2. Poll 调用 `epoll_ctl(EPOLL_CTL_ADD, fd, EPOLLIN)`
3. FDOperator 被添加到 Poll 的管理列表
4. 从此 Poll 可以监控该 FD 的读事件

---

## 3️⃣ 连接阶段（Connection）

### onConnect：初始化上下文

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

**执行时机**：
- **服务端**：Accept 后立即触发
- **客户端**：Connect 成功后触发

**典型用途**：
1. 身份验证
2. 协议握手
3. 初始化会话资源
4. 绑定自定义数据到 context

---

## 4️⃣ 工作阶段（Working）

这是 Connection 生命周期中最长的阶段，可能持续数秒到数小时。

### 读取数据流程

```
Poller 检测到 POLLIN
        │
        ▼
Poll.handler() 调用 operator.Inputs()
        │
        ▼
c.inputs() 返回 inputBuffer 的可写切片
        │
        ▼
Poll 调用 syscall.Read(fd, buf)
        │
        ▼
Poll 调用 operator.InputAck(n)
        │
        ▼
c.inputAck(n) 提交数据并触发 OnRequest
        │
        ▼
c.onRequest() 执行用户回调
        │
        ▼
用户代码处理数据（reader.Next/Peek/Skip）
```

### 写入数据流程

```
用户调用 writer.Malloc() / WriteBinary()
        │
        ▼
数据写入 outputBuffer
        │
        ▼
用户调用 writer.Flush()
        │
        ▼
c.flush() 尝试立即发送
        │
        ├─ 发送成功 → 完成
        │
        └─ 发送失败（缓冲区满）
           │
           ▼
           operator.Control(PollR2RW)  // 注册 POLLOUT
           │
           ▼
           Poller 检测到 POLLOUT
           │
           ▼
           Poll 调用 operator.Outputs()
           │
           ▼
           c.outputs() 返回 outputBuffer 的数据
           │
           ▼
           Poll 调用 syscall.Write(fd, buf)
           │
           ▼
           Poll 调用 operator.OutputAck(n)
           │
           ▼
           c.outputAck(n) 跳过已发送数据
```

### 空闲检测

```go
func (c *connection) SetIdleTimeout(timeout time.Duration) error {
    if timeout > 0 {
        // 设置 TCP keepalive
        return c.SetKeepAlive(int(timeout.Seconds()))
    }
    return nil
}
```

**keepalive 机制**：
- 设置 `SO_KEEPALIVE` socket 选项
- 内核会定期发送探测包
- 如果连接断开，触发 POLLHUP 事件

---

## 5️⃣ 关闭阶段（Closing）

Connection 的关闭可能由两方触发：**用户主动关闭** 或 **Poller 检测到异常**。

### 用户主动关闭：Close()

```go
func (c *connection) Close() error {
    return c.onClose()
}

func (c *connection) onClose() error {
    // 尝试标记为 user 关闭
    if c.closeBy(user) {
        // 成功标记，我们是第一个关闭的
        c.triggerRead(Exception(ErrConnClosed, "self close"))
        c.triggerWrite(Exception(ErrConnClosed, "self close"))
        
        // 需要主动 Detach 并执行回调
        c.closeCallback(true, true)
        return nil
    }
    
    // 已被 Poller 关闭
    c.force(closing, user)  // 修改为 user 触发
    
    // Poller 已经 Detach 了，不需要再次 Detach
    return c.closeCallback(true, false)
}
```

### Poller 检测到异常：OnHup

```go
func (c *connection) onHup(p Poll) error {
    // 尝试标记为 poller 关闭
    if !c.closeBy(poller) {
        return nil  // 已经关闭了
    }
    
    // 触发错误
    c.triggerRead(Exception(ErrEOF, "peer close"))
    c.triggerWrite(Exception(ErrConnClosed, "peer close"))
    
    // 调用 OnDisconnect
    c.onDisconnect()
    
    // 判断是否需要框架自动清理
    onConnect := c.onConnectCallback.Load()
    onRequest := c.onRequestCallback.Load()
    needCloseByUser := onConnect == nil && onRequest == nil
    
    if !needCloseByUser {
        // Poll 已经 Detach，不需要再次 Detach
        c.closeCallback(true, false)
    }
    
    return nil
}
```

### closeBy：CAS 标记关闭原因

```go
func (c *connection) closeBy(who who) bool {
    // CAS 操作：设置 closing 位并记录关闭者
    return atomic.CompareAndSwapInt64(&c.state,
        c.state & ^int64(closing),           // expected: closing 位为 0
        c.state | int64(closing) | int64(who<<8))  // new: 设置 closing 位和 who
}
```

**who 的取值**：
- `user`：用户调用 Close()
- `poller`：Poller 检测到 HUP/ERR

**为什么使用 CAS**：
- 保证只有一个执行流可以成功标记
- 防止重复关闭
- 记录关闭原因（用于后续逻辑判断）

---

## 6️⃣ 清理阶段（Cleanup）

### closeCallback：执行回调并释放资源

```go
func (c *connection) closeCallback(needLock, needDetach bool) (err error) {
    // 1. 获取 processing 锁（如果需要）
    if needLock && !c.lock(processing) {
        return nil
    }
    
    // 2. 从 Poll 中分离（如果需要）
    if needDetach && c.operator.poll != nil {
        if err := c.operator.Control(PollDetach); err != nil {
            logger.Printf("NETPOLL: closeCallback detach operator failed: %v", err)
        }
    }
    
    // 3. 执行所有 CloseCallback（逆序）
    latest := c.closeCallbacks.Load()
    if latest == nil {
        return nil
    }
    for callback := latest.(*callbackNode); callback != nil; callback = callback.pre {
        callback.fn(c)
    }
    
    // 4. 关闭缓冲区
    c.closeBuffer()
    
    return nil
}
```

### closeBuffer：清理缓冲区

```go
func (c *connection) closeBuffer() {
    onConnect, _ := c.onConnectCallback.Load().(OnConnect)
    onRequest, _ := c.onRequestCallback.Load().(OnRequest)
    
    // 检查是否可以安全关闭 inputBuffer
    if c.inputBuffer.Len() == 0 || onConnect != nil || onRequest != nil {
        c.inputBuffer.Close()  // 归还到对象池
    }
    
    // 检查是否可以安全关闭 outputBuffer
    if c.outputBuffer.Len() == 0 || onConnect != nil || onRequest != nil {
        c.outputBuffer.Close()
        barrierPool.Put(c.outputBarrier)  // 归还 barrier
    }
}
```

**为什么要检查 Len()**：
- 如果是用户主动关闭，且 Buffer 中还有数据
- 说明是"不干净"的关闭
- 不能归还到对象池（可能有其他 goroutine 在访问）
- 让 GC 回收这些 Buffer

### Control(PollDetach)：从 Poll 中分离

```go
func (op *FDOperator) Control(event PollEvent) error {
    if event == PollDetach && atomic.AddInt32(&op.detached, 1) > 1 {
        return nil  // 已经 detach 了
    }
    return op.poll.Control(op, event)
}
```

**Poll.Control(op, PollDetach) 内部**：
1. 调用 `epoll_ctl(EPOLL_CTL_DEL, fd, nil)`
2. 从 Poll 的管理列表中移除 FDOperator
3. 关闭 fd：`syscall.Close(fd)`

---

## 🎭 关闭场景分析

### 场景 1：用户主动关闭（正常流程）

```
Time 1: 用户调用 conn.Close()
Time 2: closeBy(user) 成功
Time 3: triggerRead/triggerWrite 唤醒阻塞的 goroutine
Time 4: closeCallback(true, true)
        ├─ 获取 processing 锁
        ├─ Control(PollDetach)
        ├─ 执行 CloseCallback
        └─ closeBuffer()
Time 5: 资源清理完毕
```

### 场景 2：对端关闭（Peer Close）

```
Time 1: 对端调用 close()
Time 2: 内核发送 FIN 包
Time 3: 本地内核接收 FIN，标记 socket 为可读
Time 4: epoll_wait 返回 POLLIN | POLLHUP
Time 5: Poll 调用 operator.OnHup()
        ├─ c.onHup(p)
        ├─ closeBy(poller) 成功
        ├─ triggerRead/triggerWrite
        ├─ onDisconnect()
        └─ closeCallback(true, false)  // Poll 已经 Detach
Time 6: 资源清理完毕
```

### 场景 3：连接错误（Connection Error）

```
Time 1: 网络异常（如对端 RST）
Time 2: epoll_wait 返回 POLLERR | POLLHUP
Time 3: Poll 调用 operator.OnHup()
        ├─ c.onHup(p)
        └─ ... （同场景 2）
```

### 场景 4：并发关闭（User + Poller）

```
Time 1: 用户调用 conn.Close()
Time 2: closeBy(user) 成功
Time 3: closeCallback 开始执行
Time 4: （同时）Poller 检测到 HUP
Time 5: closeBy(poller) 失败（已被标记为 user）
Time 6: onHup 直接返回
Time 7: 用户的 closeCallback 继续执行
Time 8: 资源清理完毕
```

**CAS 的作用**：
- 保证只有一个执行流可以进入清理逻辑
- 防止资源被释放两次

### 场景 5：OnRequest 中关闭

```
Time 1: OnRequest 正在执行（持有 processing 锁）
Time 2: OnRequest 中调用 conn.Close()
Time 3: closeBy(user) 成功
Time 4: closeCallback(true, true) 尝试获取 processing 锁
Time 5: 获取失败，直接返回
Time 6: OnRequest 执行完毕
Time 7: onProcess 中检测到 closedBy == user
Time 8: onProcess 调用 closeCallback(false, true)
Time 9: 资源清理完毕
```

**设计精妙之处**：
- `closeCallback(needLock=true)` 尝试获取锁，失败则返回
- `onProcess` 会在退出前检查是否被关闭
- 保证 closeCallback 最终被调用

---

## 🛡️ 资源清理检查清单

### 必须清理的资源

1. **文件描述符（FD）**
   - ✅ 通过 `Control(PollDetach)` 关闭
   - ✅ Poll 内部调用 `syscall.Close(fd)`

2. **LinkBuffer**
   - ✅ `inputBuffer.Close()` 归还节点到对象池
   - ✅ `outputBuffer.Close()` 归还节点到对象池

3. **outputBarrier**
   - ✅ `barrierPool.Put(c.outputBarrier)` 归还到池

4. **FDOperator**
   - ✅ 从 Poll 的管理列表中移除
   - ❓ 不需要归还到对象池（由 Poll 管理）

5. **goroutine**
   - ✅ `triggerRead/triggerWrite` 唤醒阻塞的 goroutine
   - ✅ 阻塞的 goroutine 收到错误后退出

6. **channel**
   - ❓ 不需要关闭（goroutine 退出后 GC 会回收）

7. **Timer**
   - ✅ `readTimer/writeTimer` 会在 waitRead/waitWrite 中停止

### 可能泄漏的资源

1. **用户在 OnConnect 中分配的资源**
   - ⚠️ 必须通过 `AddCloseCallback` 清理

2. **用户在 context 中绑定的资源**
   - ⚠️ 必须通过 `AddCloseCallback` 清理

3. **长期持有的 Next() 返回值**
   - ⚠️ 如果用户长期持有，会阻止 LinkBuffer 节点释放

---

## 🚨 常见的生命周期错误

### 错误 1：忘记 Release

```go
// ❌ 错误示例
func OnRequest(ctx context.Context, conn Connection) error {
    data, _ := conn.Reader().Next(100)
    process(data)
    return nil  // 忘记 Release
}
```

**后果**：
- inputBuffer 的节点无法释放
- 内存持续增长
- 最终 OOM

**正确做法**：
```go
// ✅ 正确示例
func OnRequest(ctx context.Context, conn Connection) error {
    defer conn.Reader().Release()
    
    for conn.Reader().Len() > 0 {
        data, _ := conn.Reader().Next(100)
        process(data)
    }
    return nil
}
```

### 错误 2：在 CloseCallback 中访问 Reader/Writer

```go
// ❌ 错误示例
conn.AddCloseCallback(func(c Connection) error {
    data, _ := c.Reader().Next(10)  // 已经关闭了！
    // ...
})
```

**后果**：
- 读取失败或返回旧数据
- 可能 panic

**正确做法**：
```go
// ✅ 正确示例
conn.AddCloseCallback(func(c Connection) error {
    // 只访问自定义资源
    session := getSession(c)
    session.Close()
    return nil
})
```

### 错误 3：在 OnRequest 中长时间阻塞

```go
// ❌ 错误示例
func OnRequest(ctx context.Context, conn Connection) error {
    data, _ := conn.Reader().Next(100)
    
    // 长时间阻塞（如数据库查询）
    result := db.Query(data)  // 10 秒
    
    conn.Writer().WriteBinary(result)
    conn.Writer().Flush()
    return nil
}
```

**后果**：
- OnRequest 串行执行，阻塞期间无法处理新数据
- 新数据堆积在 inputBuffer 中
- 可能触发超时

**正确做法**：
```go
// ✅ 正确示例
func OnRequest(ctx context.Context, conn Connection) error {
    data, _ := conn.Reader().Next(100)
    conn.Reader().Release()
    
    // 在新 goroutine 中处理
    go func() {
        result := db.Query(data)
        // 注意：需要自己处理并发写入
        conn.Writer().WriteBinary(result)
        conn.Writer().Flush()
    }()
    
    return nil
}
```

### 错误 4：假设 OnConnect 一定会执行

```go
// ❌ 错误示例
var session *Session

func MyOnConnect(ctx context.Context, conn Connection) context.Context {
    session = &Session{}
    return context.WithValue(ctx, "session", session)
}

func MyOnRequest(ctx context.Context, conn Connection) error {
    s := ctx.Value("session").(*Session)  // 可能 panic
    // ...
}
```

**问题**：
- 如果 Accept 后立即收到数据，可能先触发 OnRequest
- OnConnect 可能还没执行

**正确做法**：
```go
// ✅ 正确示例
func MyOnRequest(ctx context.Context, conn Connection) error {
    s, ok := ctx.Value("session").(*Session)
    if !ok {
        // OnConnect 还没执行，等待
        return nil
    }
    // ...
}
```

---

## 📊 生命周期调试技巧

### 1. 添加日志

```go
conn.AddCloseCallback(func(c Connection) error {
    log.Printf("Connection closed: %s -> %s, duration: %v",
        c.LocalAddr(), c.RemoteAddr(), time.Since(startTime))
    return nil
})
```

### 2. 使用 pprof 检查 goroutine 泄漏

```bash
# 获取 goroutine profile
curl http://localhost:6060/debug/pprof/goroutine > goroutine.txt

# 查找可疑的 goroutine
grep "netpoll" goroutine.txt
```

### 3. 监控 FD 数量

```bash
# Linux
ls -l /proc/$(pidof myapp)/fd | wc -l

# 如果 FD 数量持续增长，说明有连接泄漏
```

### 4. 使用 Race Detector

```bash
go run -race main.go

# 可以检测并发访问 Connection 的问题
```

---

## 🔍 总结

Connection 的生命周期管理体现了系统编程的复杂性：

1. **多阶段协调**
   - 创建 → 准备 → 连接 → 工作 → 关闭 → 清理
   - 每个阶段都有特定的职责

2. **资源精确管理**
   - FD、Buffer、Timer、Channel 等
   - 通过对象池复用，减少 GC 压力

3. **复杂的关闭语义**
   - 用户关闭 vs Poller 关闭
   - 正常关闭 vs 异常关闭
   - 通过 CAS 保证只清理一次

4. **并发安全保证**
   - locker 位标志
   - atomic 操作
   - 双重检查机制

5. **优雅的错误处理**
   - Panic 恢复
   - 资源不泄漏
   - 回调保证执行

理解 Connection 的生命周期，是正确使用 Netpoll 和避免资源泄漏的关键。

