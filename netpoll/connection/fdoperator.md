# Connection 与 FDOperator 的深度结合

FDOperator 是 Netpoll 中连接 Poll（事件循环）和 Connection（应用层）的关键桥梁。它定义了一套标准的接口，使得 Poll 可以以通用的方式处理所有的文件描述符事件。

## 🎯 FDOperator 的设计目标

1. **解耦**：Poll 不需要知道 Connection 的具体实现
2. **通用性**：不仅支持 Connection，还可以支持 Listener 等其他 FD
3. **高效**：通过函数指针直接调用，避免接口开销
4. **状态管理**：通过 CAS 操作保证并发安全

---

## 📐 FDOperator 结构详解

```go
type FDOperator struct {
    // 文件描述符
    FD int
    
    // ============ 事件回调（由 Poll 调用）============
    
    // OnRead: 当 FD 可读时触发（POLLIN 事件）
    OnRead  func(p Poll) error
    
    // OnWrite: 当 FD 可写时触发（POLLOUT 事件）
    OnWrite func(p Poll) error
    
    // OnHup: 当对端关闭时触发（POLLHUP 事件）
    OnHup   func(p Poll) error
    
    // ============ 数据传输接口（由 Poll 调用）============
    
    // Inputs: Poll 调用此方法获取可写入的缓冲区
    // 参数 vs: Poll 提供的切片数组（用于复用）
    // 返回 rs: 实际可写入的缓冲区列表
    Inputs   func(vs [][]byte) (rs [][]byte)
    
    // InputAck: Poll 实际读取了 n 字节后，调用此方法确认
    // 参数 n: 实际读取的字节数
    InputAck func(n int) (err error)
    
    // Outputs: Poll 调用此方法获取待发送的数据
    // 参数 vs: Poll 提供的切片数组（用于复用）
    // 返回 rs: 待发送的数据块列表
    // 返回 supportZeroCopy: 是否支持零拷贝（当前未实现）
    Outputs   func(vs [][]byte) (rs [][]byte, supportZeroCopy bool)
    
    // OutputAck: Poll 实际发送了 n 字节后，调用此方法确认
    // 参数 n: 实际发送的字节数
    OutputAck func(n int) (err error)
    
    // ============ Poll 管理 ============
    
    // poll: FDOperator 注册到的 Poll 实例
    poll Poll
    
    // detached: 防止重复 detach（原子操作）
    detached int32
    
    // ============ 对象池管理（internal）============
    
    // next: 链表指针，用于 operatorCache
    next  *FDOperator
    
    // state: CAS 状态 
    // 0 = unused（在对象池中）
    // 1 = inuse（正在使用）
    // 2 = do-done（临时状态，用于 do/done 机制）
    state int32
    
    // index: 在 operatorCache 中的索引
    index int32
}
```

---

## 🔗 Connection 如何初始化 FDOperator

### 1. 创建阶段

当创建一个新的 Connection 时（无论是 Accept 还是 Dial），都会初始化 FDOperator：

```go
func newConnection(fd int) *connection {
    c := &connection{
        netFD:        netFD{fd: fd},
        inputBuffer:  NewLinkBuffer(),
        outputBuffer: NewLinkBuffer(),
        outputBarrier: barrierPool.Get().(*barrier),
        bookSize:     defaultLinkBufferSize,  // 初始 4KB
        maxSize:      defaultLinkBufferSize,
    }
    
    // 创建 FDOperator
    c.operator = &FDOperator{
        FD:        fd,
        // 绑定 inputs/outputs 接口
        Inputs:    c.inputs,
        InputAck:  c.inputAck,
        Outputs:   c.outputs,
        OutputAck: c.outputAck,
    }
    
    return c
}
```

### 2. 注册阶段

Connection 初始化完成后，需要注册到 Poll：

```go
func (c *connection) onPrepare(opts *options) (err error) {
    // 设置回调
    c.SetOnConnect(opts.onConnect)
    c.SetOnRequest(opts.onRequest)
    // ...
    
    // 注册到 Poll
    if c.IsActive() {
        return c.register()
    }
    return nil
}

func (c *connection) register() (err error) {
    // 控制 FDOperator，注册为可读事件
    err = c.operator.Control(PollReadable)
    if err != nil {
        logger.Printf("NETPOLL: connection register failed: %v", err)
        c.Close()
        return Exception(ErrConnClosed, err.Error())
    }
    return nil
}
```

**Control 方法**：

```go
func (op *FDOperator) Control(event PollEvent) error {
    // 特殊处理 PollDetach：防止重复 detach
    if event == PollDetach && atomic.AddInt32(&op.detached, 1) > 1 {
        return nil
    }
    // 委托给 Poll
    return op.poll.Control(op, event)
}
```

---

## 📥 输入流程：Inputs & InputAck

### Inputs：提供缓冲区

当 epoll_wait 检测到 FD 可读时，Poll 会调用 `Inputs` 获取可写入的缓冲区。

```go
func (c *connection) inputs(vs [][]byte) (rs [][]byte) {
    // 从 inputBuffer 预订内存
    // bookSize: 期望读取的大小（动态调整，初始 4KB）
    // maxSize:  最大允许的大小（防止单次读取过多）
    vs[0] = c.inputBuffer.book(c.bookSize, c.maxSize)
    return vs[:1]
}
```

**LinkBuffer.book 的作用**：
1. **检查当前 write 节点剩余空间**
   - 如果剩余空间 >= bookSize，直接返回
   - 如果剩余空间 < bookSize 但 > 0，返回剩余空间
   - 如果剩余空间 = 0，分配新节点（大小为 maxSize）

2. **返回可写切片**
   - 返回的是 `write.buf[write.malloc:]` 的一部分
   - **零拷贝**：Poll 直接往这个切片写入

**向量化 I/O**：
- 虽然当前实现只返回一个切片（`vs[:1]`）
- 但接口设计支持返回多个切片，配合 `readv` 系统调用
- 未来可以优化为一次读取到多个不连续的节点

### InputAck：确认读取

Poll 实际读取了 `n` 字节后，调用 `InputAck` 确认：

```go
func (c *connection) inputAck(n int) (err error) {
    if n <= 0 {
        c.inputBuffer.bookAck(0)
        return nil
    }
    
    // ========== 1. 自适应调整 bookSize ==========
    // 如果读满了，说明可能还有更多数据，翻倍预订大小
    if n == c.bookSize && c.bookSize < mallocMax {
        c.bookSize <<= 1  // 指数增长：4KB -> 8KB -> 16KB -> ... -> 8MB
    }
    
    // ========== 2. 提交数据到 inputBuffer ==========
    // bookAck 会：
    //   1. 更新 write.malloc 指针
    //   2. 扩展 write.buf 的长度
    //   3. 更新 flush 指针（标记为可读）
    //   4. 原子更新 inputBuffer.length
    length, _ := c.inputBuffer.bookAck(n)
    
    // ========== 3. 更新 maxSize ==========
    if c.maxSize < length {
        c.maxSize = length
    }
    if c.maxSize > mallocMax {
        c.maxSize = mallocMax
    }
    
    // ========== 4. 触发 OnRequest 或唤醒 Reader ==========
    needTrigger := true
    if length == n {  
        // 首次接收数据（inputBuffer 之前为空）
        // 触发 OnRequest 回调
        needTrigger = c.onRequest()
    }
    
    // 检查是否需要唤醒阻塞的 Reader
    // waitReadSize 是 Reader 期望的数据量（由 waitRead 设置）
    if needTrigger && length >= int(atomic.LoadInt64(&c.waitReadSize)) {
        c.triggerRead(nil)
    }
    
    return nil
}
```

**关键逻辑分析**：

#### 1. 自适应 bookSize

```
初始：bookSize = 4KB
第一次读取：读取了 4KB -> bookSize = 8KB
第二次读取：读取了 8KB -> bookSize = 16KB
第三次读取：读取了 12KB（没读满） -> bookSize 保持 16KB
...
最大：bookSize = 8MB (mallocMax)
```

**好处**：
- 小流量场景：避免浪费内存
- 大流量场景：快速适应，减少系统调用次数

#### 2. 首次数据的特殊处理

```go
if length == n {
    needTrigger = c.onRequest()
}
```

- `length == n` 表示 `inputBuffer` 之前为空（length 的增量等于读取量）
- 这时需要触发 `OnRequest` 启动处理流程
- `onRequest()` 返回 false 表示已经启动了处理任务，不需要再 `triggerRead`

#### 3. triggerRead 的时机

```go
if needTrigger && length >= int(atomic.LoadInt64(&c.waitReadSize)) {
    c.triggerRead(nil)
}
```

- 只有当数据量满足 Reader 的期望时才唤醒
- 避免频繁唤醒导致的 CPU 浪费

---

## 📤 输出流程：Outputs & OutputAck

### Outputs：提供待发送数据

当 epoll_wait 检测到 FD 可写时，Poll 会调用 `Outputs` 获取待发送的数据。

```go
func (c *connection) outputs(vs [][]byte) (rs [][]byte, _ bool) {
    // 检查 outputBuffer 是否为空
    if c.outputBuffer.IsEmpty() {
        c.rw2r()  // 切换为只监听读事件
        return rs, false
    }
    
    // 获取所有待发送的数据块（向量化 I/O）
    rs = c.outputBuffer.GetBytes(vs)
    return rs, false
}
```

**outputBuffer.GetBytes 的作用**：
- 返回多个不连续的内存块（LinkBuffer 的链表节点）
- 配合 `writev` 系统调用，实现零拷贝发送
- Poll 无需关心数据的物理布局

**示例**：
```
outputBuffer 内部：
┌──────┐    ┌──────┐    ┌──────┐
│Node 1│ -> │Node 2│ -> │Node 3│
│ 100B │    │ 200B │    │ 150B │
└──────┘    └──────┘    └──────┘

GetBytes 返回：
rs[0] = Node1.buf[Node1.off:Node1.malloc]  // 100 bytes
rs[1] = Node2.buf[Node2.off:Node2.malloc]  // 200 bytes  
rs[2] = Node3.buf[Node3.off:Node3.malloc]  // 150 bytes

writev(fd, rs, 3)  // 一次系统调用发送 450 bytes
```

### OutputAck：确认发送

Poll 实际发送了 `n` 字节后，调用 `OutputAck` 确认：

```go
func (c *connection) outputAck(n int) (err error) {
    if n > 0 {
        // 跳过已发送的数据
        c.outputBuffer.Skip(n)
        // 释放完全读取的节点
        c.outputBuffer.Release()
    }
    
    // 检查是否全部发送完毕
    if c.outputBuffer.IsEmpty() {
        c.rw2r()  // 停止监听写事件
    }
    
    return nil
}
```

**rw2r 方法**：

```go
func (c *connection) rw2r() {
    // 从 POLLIN | POLLOUT 切换为 POLLIN
    c.operator.Control(PollRW2R)
    // 唤醒可能在等待的 Writer
    c.triggerWrite(nil)
}
```

**为什么要切换事件**：
- 如果没有数据要发送，持续监听 POLLOUT 会导致频繁的虚假唤醒
- Linux 下，socket 缓冲区不满时，POLLOUT 总是触发
- 切换为只监听 POLLIN 可以节省 CPU

---

## 🎭 事件回调：OnRead / OnWrite / OnHup

FDOperator 的事件回调由 Connection 的事件处理方法实现（在 `connection_reactor.go`）。

### OnHup：处理对端关闭

```go
func (c *connection) onHup(p Poll) error {
    // 尝试标记为 poller 关闭
    if !c.closeBy(poller) {
        return nil  // 已经被关闭了
    }
    
    // 触发读写错误，唤醒阻塞的 goroutine
    c.triggerRead(Exception(ErrEOF, "peer close"))
    c.triggerWrite(Exception(ErrConnClosed, "peer close"))
    
    // 调用 OnDisconnect 回调
    c.onDisconnect()
    
    // 判断是否需要由用户主动关闭
    onConnect := c.onConnectCallback.Load()
    onRequest := c.onRequestCallback.Load()
    needCloseByUser := onConnect == nil && onRequest == nil
    
    if !needCloseByUser {
        // 已经 PollDetach，不需要再次 Detach
        c.closeCallback(true, false)
    }
    
    return nil
}
```

**关键点**：
1. **closeBy(poller)**：通过 CAS 操作标记关闭原因，防止重复处理
2. **triggerRead/triggerWrite**：唤醒所有阻塞的 Reader/Writer
3. **needCloseByUser 判断**：
   - 如果没有设置 OnRequest，说明是简单的 echo 服务，用户需要自己关闭
   - 如果设置了 OnRequest，框架会自动清理资源

### OnRead / OnWrite

这两个回调在 Connection 中没有直接实现，而是由 Poll 隐式处理：
- **OnRead**：Poll 检测到 POLLIN 后，直接调用 `Inputs` 和 `InputAck`
- **OnWrite**：Poll 检测到 POLLOUT 后，直接调用 `Outputs` 和 `OutputAck`

---

## 🔄 状态管理：do/done 机制

FDOperator 使用 CAS 操作实现了一个简单的状态机，用于 `Release()` 操作的并发控制。

```go
// state 的三种状态
const (
    stateUnused = 0  // 在对象池中，未使用
    stateInuse  = 1  // 正在使用
    stateDoDone = 2  // 临时状态，do() 获取，done() 释放
)
```

### do/done 的使用场景

```go
func (c *connection) Release() (err error) {
    // 优化：只有在 inputBuffer 为空时才需要处理 maxSize
    if c.inputBuffer.Len() == 0 && c.operator.do() {
        // 获取 do 锁成功
        
        maxSize := c.inputBuffer.calcMaxSize()
        if maxSize > mallocMax {
            maxSize = mallocMax
        }
        
        if maxSize > c.maxSize {
            c.maxSize = maxSize
        }
        
        if c.inputBuffer.Len() == 0 {
            c.inputBuffer.resetTail(c.maxSize)
        }
        
        // 释放 do 锁
        c.operator.done()
    }
    
    return c.inputBuffer.Release()
}
```

**为什么需要 do/done**：

考虑以下竞态场景：
```
时间线 1: User Goroutine               时间线 2: Poll Goroutine
        |                                       |
        | c.Release()                           |
        | ├─ inputBuffer.Len() == 0            |
        | ├─ calcMaxSize()                     |
        | └─ inputBuffer.resetTail()          |
        |                                       | inputs()  
        |                                       | └─ inputBuffer.book()  
        |                                           ↑ 竞态！
```

- `resetTail()` 会修改 inputBuffer 的 write 指针
- `book()` 也会操作 write 指针
- 如果并发执行，可能导致数据损坏

**do/done 机制的保护**：

```go
func (op *FDOperator) do() (can bool) {
    // CAS: state 从 1 (inuse) 切换为 2 (do-done)
    return atomic.CompareAndSwapInt32(&op.state, 1, 2)
}

func (op *FDOperator) done() {
    // 恢复: state 从 2 (do-done) 切换回 1 (inuse)
    atomic.StoreInt32(&op.state, 1)
}
```

- Poll 在调用 `inputs()` 前会检查 `state != 2`
- 如果 `state == 2`，说明有人在执行 `Release()`，Poll 会跳过
- 这样就避免了竞态条件

---

## 🔁 事件切换：Control 方法

FDOperator 提供了统一的事件控制接口：

```go
func (op *FDOperator) Control(event PollEvent) error {
    // 特殊处理 Detach
    if event == PollDetach && atomic.AddInt32(&op.detached, 1) > 1 {
        return nil  // 已经 detach 了，忽略
    }
    // 委托给 Poll
    return op.poll.Control(op, event)
}
```

### 常用的事件类型

```go
const (
    // 只监听读事件
    PollReadable PollEvent = 0x1  // POLLIN
    
    // 监听读+写事件
    PollWritable PollEvent = 0x2  // POLLIN | POLLOUT
    
    // 从只读切换到读写
    PollR2RW PollEvent = 0x3
    
    // 从读写切换到只读
    PollRW2R PollEvent = 0x4
    
    // 从 Poll 中分离（不再监听任何事件）
    PollDetach PollEvent = 0x5
)
```

### 事件切换的时机

1. **注册时**：`PollReadable`
   ```go
   c.operator.Control(PollReadable)
   ```

2. **首次写入**：`PollR2RW`
   ```go
   func (c *connection) flush() error {
       // ...
       return c.operator.Control(PollR2RW)
   }
   ```

3. **写完毕**：`PollRW2R`
   ```go
   func (c *connection) outputAck(n int) error {
       // ...
       if c.outputBuffer.IsEmpty() {
           c.operator.Control(PollRW2R)
       }
       return nil
   }
   ```

4. **关闭时**：`PollDetach`
   ```go
   c.operator.Control(PollDetach)
   ```

---

## 🎯 FDOperator 的对象池管理

为了减少 GC 压力，FDOperator 使用了对象池（`operatorCache`）。

### operatorCache 结构

```go
type operatorCache struct {
    cache  []atomic.Value  // 存储 *FDOperator
    freelist *FDOperator   // 空闲链表
}
```

### inuse / unused 方法

```go
// inuse: 从对象池中取出（标记为使用中）
func (op *FDOperator) inuse() {
    for !atomic.CompareAndSwapInt32(&op.state, 0, 1) {
        if atomic.LoadInt32(&op.state) == 1 {
            return  // 已经是 inuse 了
        }
        runtime.Gosched()  // 让出 CPU
    }
}

// unused: 归还到对象池（标记为未使用）
func (op *FDOperator) unused() {
    for !atomic.CompareAndSwapInt32(&op.state, 1, 0) {
        if atomic.LoadInt32(&op.state) == 0 {
            return  // 已经是 unused 了
        }
        runtime.Gosched()
    }
}
```

### 生命周期

```
1. 从池中获取 -> inuse()
2. 设置 FD、回调等
3. 注册到 Poll
4. ... 使用 ...
5. 从 Poll 中分离 -> Control(PollDetach)
6. 重置 -> reset()
7. 归还到池 -> unused()
```

---

## 🔍 性能优化细节

### 1. 向量化 I/O

```go
// 一次 writev 调用发送多个不连续的内存块
rs := c.outputBuffer.GetBytes(vs)
n, err := writev(c.fd, rs)
```

**好处**：
- 减少系统调用次数
- 避免内存拷贝（不需要将多个块合并）

### 2. 事件精确控制

```go
// 没有数据时，不监听 POLLOUT
if c.outputBuffer.IsEmpty() {
    c.operator.Control(PollRW2R)
}
```

**好处**：
- 避免虚假唤醒
- 降低 CPU 使用率

### 3. 自适应缓冲区

```go
// 根据实际读取量动态调整
if n == c.bookSize && c.bookSize < mallocMax {
    c.bookSize <<= 1
}
```

**好处**：
- 小流量场景：节省内存
- 大流量场景：减少系统调用

### 4. CAS 无锁设计

```go
// do/done 使用 CAS，避免互斥锁开销
func (op *FDOperator) do() (can bool) {
    return atomic.CompareAndSwapInt32(&op.state, 1, 2)
}
```

**好处**：
- 低延迟
- 高并发性能

---

## 📊 总结

FDOperator 是 Netpoll 架构中的关键抽象，它：

1. **解耦了 Poll 和 Connection**
   - Poll 只需要操作 FDOperator 接口
   - Connection 实现接口的具体逻辑

2. **实现了零拷贝 I/O**
   - `Inputs` 直接提供 LinkBuffer 的内部切片
   - `Outputs` 返回多个不连续的内存块

3. **提供了高效的状态管理**
   - CAS 操作实现无锁并发控制
   - 对象池减少 GC 压力

4. **支持灵活的事件切换**
   - 精确控制 POLLIN / POLLOUT 监听
   - 避免不必要的事件触发

理解 FDOperator，就理解了 Netpoll 如何将高性能的事件循环与易用的 Connection 接口完美结合。

