# Connection 的事件处理机制深度解析

Connection 的事件处理机制是 Netpoll 中最复杂也是最精妙的部分。它通过一套精心设计的回调系统和状态机，实现了高效、安全的异步事件处理。

## 🎯 事件处理的设计目标

1. **串行保证**：同一个 Connection 的 OnRequest 必须串行执行
2. **高效触发**：数据到达时能快速触发处理
3. **生命周期管理**：正确处理连接建立、数据传输、连接关闭的全流程
4. **Panic 安全**：用户代码 Panic 不能导致资源泄漏
5. **并发安全**：Poller 和用户 goroutine 可能并发访问 Connection

---

## 📐 onEvent 结构体

```go
type onEvent struct {
    ctx                  context.Context  // 连接上下文
    onConnectCallback    atomic.Value     // OnConnect 回调
    onDisconnectCallback atomic.Value     // OnDisconnect 回调
    onRequestCallback    atomic.Value     // OnRequest 回调
    closeCallbacks       atomic.Value     // CloseCallback 链表（最新）
}
```

**atomic.Value 的使用**：
- 支持无锁的并发读写
- 允许动态设置/替换回调
- 避免了互斥锁的开销

### CloseCallback 链表

```go
type callbackNode struct {
    fn  CloseCallback          // 回调函数
    pre *callbackNode          // 前一个节点（构成链表）
}
```

**链表设计**：
- 新的 CloseCallback 添加到链表头部
- 执行时逆序遍历（LIFO）
- 最后添加的最先执行

---

## 🔄 连接状态机

```go
type connState = int32

const (
    connStateNone         = 0  // 初始状态
    connStateConnected    = 1  // 已连接（OnConnect 已执行）
    connStateDisconnected = 2  // 已断开（OnDisconnect 已执行）
)
```

### 状态转换图

```
                    创建 Connection
                           │
                           ▼
                   ┌───────────────┐
                   │     None      │ (初始状态)
                   │   (state=0)   │
                   └───────┬───────┘
                           │
                    OnConnect 执行
                           │
                           ▼
                   ┌───────────────┐
                   │   Connected   │ (正常工作)
                   │   (state=1)   │
                   └───────┬───────┘
                           │
                    连接关闭
                           │
                           ▼
                   ┌───────────────┐
                   │ Disconnected  │ (终态)
                   │   (state=2)   │
                   └───────────────┘
```

### 状态操作方法

```go
// 获取当前状态
func (c *connection) getState() connState {
    return connState(atomic.LoadInt32(&c.state))
}

// 设置状态（无条件）
func (c *connection) setState(state connState) {
    atomic.StoreInt32(&c.state, int32(state))
}

// CAS 修改状态（只有从 old 才能变为 new）
func (c *connection) changeState(old, new connState) bool {
    return atomic.CompareAndSwapInt32(&c.state, int32(old), int32(new))
}
```

---

## 🚀 OnConnect：连接建立时的初始化

### 执行时机

OnConnect 在以下情况被触发：
1. **服务端**：Accept 新连接后
2. **客户端**：Dial 成功后

### onConnect 方法

```go
func (c *connection) onConnect() {
    onConnect, _ := c.onConnectCallback.Load().(OnConnect)
    
    // 没有设置 OnConnect，直接标记为已连接
    if onConnect == nil {
        c.changeState(connStateNone, connStateConnected)
        return
    }
    
    // 获取 connecting 锁
    if !c.lock(connecting) {
        // 永远不会失败（因为 onDisconnect 会检查状态）
        return
    }
    
    // 获取 OnRequest 回调
    onRequest, _ := c.onRequestCallback.Load().(OnRequest)
    
    // 执行 OnConnect 和可能的 OnRequest
    c.onProcess(onConnect, onRequest)
}
```

**关键点**：
1. **状态检查**：通过 `changeState` 保证 OnConnect 只执行一次
2. **connecting 锁**：保护 OnConnect 执行期间的状态
3. **传递 OnRequest**：如果连接建立时已有数据，需要立即处理

### OnConnect 的典型用途

```go
func MyOnConnect(ctx context.Context, conn Connection) context.Context {
    // 1. 认证检查
    if !authenticate(conn) {
        conn.Close()
        return ctx
    }
    
    // 2. 初始化连接级资源
    session := &Session{
        ID:     generateID(),
        Conn:   conn,
        Buffer: make([]byte, 4096),
    }
    
    // 3. 绑定到 context
    ctx = context.WithValue(ctx, "session", session)
    
    // 4. 注册清理回调
    conn.AddCloseCallback(func(conn Connection) error {
        session.Close()
        return nil
    })
    
    return ctx
}
```

---

## 📨 OnRequest：数据到达时的处理

### 执行时机

OnRequest 在以下情况被触发：
1. **首次数据到达**：`inputAck` 中检测到 `length == n`
2. **后续数据到达**：`inputAck` 中检测到 `length >= waitReadSize`
3. **onProcess 循环**：处理完一批数据后，检测到还有数据

### onRequest 方法

```go
func (c *connection) onRequest() (needTrigger bool) {
    onRequest, ok := c.onRequestCallback.Load().(OnRequest)
    if !ok {
        return true  // 没有设置 OnRequest
    }
    
    // 等待 OnConnect 完成
    if c.getState() == connStateNone && c.onConnectCallback.Load() != nil {
        // 让 OnConnect 帮我们调用 OnRequest
        return
    }
    
    // 执行 OnRequest
    processed := c.onProcess(nil, onRequest)
    
    // 如果没有处理（已有任务在运行），需要触发 triggerRead
    return !processed
}
```

**关键判断**：
1. **OnConnect 未完成**：不能执行 OnRequest，等待 OnConnect
2. **processing 锁被占用**：已有任务在运行，返回 false

### OnRequest 的典型用途

```go
func MyOnRequest(ctx context.Context, conn Connection) error {
    reader := conn.Reader()
    defer reader.Release()
    
    for reader.Len() > 0 {
        // 1. 读取协议头
        if reader.Len() < 4 {
            break  // 数据不完整，等待更多数据
        }
        
        header, _ := reader.Peek(4)
        packetLen := binary.BigEndian.Uint32(header)
        
        // 2. 检查完整包
        if reader.Len() < int(packetLen) {
            break  // 等待完整的包
        }
        
        // 3. 读取并处理完整包
        packet, _ := reader.Next(int(packetLen))
        processPacket(ctx, packet)
    }
    
    return nil
}
```

---

## 🔁 onProcess：核心处理循环

这是 Netpoll 事件处理的核心，它负责：
1. 串行执行 OnConnect 和 OnRequest
2. 循环处理所有可读数据
3. 正确处理连接关闭
4. Panic 恢复和资源清理

### onProcess 完整流程

```go
func (c *connection) onProcess(onConnect OnConnect, onRequest OnRequest) (processed bool) {
    // ========== 1. 获取 processing 锁 ==========
    if !c.lock(processing) {
        return false  // 已经有任务在运行
    }
    
    task := func() {
        panicked := true  // 默认假设会 panic
        
        defer func() {
            if !panicked {
                return  // 正常退出，不处理
            }
            
            // ========== Panic 恢复 ==========
            c.unlock(processing)
            if c.IsActive() {
                c.Close()  // 主动关闭连接
            } else {
                c.closeCallback(false, false)  // 已关闭，只执行回调
            }
        }()
        
        // ========== 2. 执行 OnConnect（如果存在） ==========
        if onConnect != nil && c.changeState(connStateNone, connStateConnected) {
            c.ctx = onConnect(c.ctx, c)
            
            // 检查 OnConnect 中是否关闭了连接
            if !c.IsActive() && c.changeState(connStateConnected, connStateDisconnected) {
                // 触发 OnDisconnect
                onDisconnect, _ := c.onDisconnectCallback.Load().(OnDisconnect)
                if onDisconnect != nil {
                    onDisconnect(c.ctx, c)
                }
            }
            c.unlock(connecting)
        }
        
    START:
        // ========== 3. 执行 OnRequest（至少一次，如果有数据） ==========
        if onRequest != nil && c.Reader().Len() > 0 {
            _ = onRequest(c.ctx, c)
        }
        
        // ========== 4. 循环处理数据 ==========
        var closedBy who
        for {
            closedBy = c.status(closing)
            
            // 退出条件
            if closedBy == user ||           // 用户关闭
               onRequest == nil ||           // 没有回调
               c.Reader().Len() == 0 {      // 没有数据
                break
            }
            
            // 继续处理数据
            _ = onRequest(c.ctx, c)
        }
        
        // ========== 5. 处理关闭 ==========
        if closedBy != none {
            needDetach := closedBy == user  // 用户关闭需要 Detach
            c.closeCallback(false, needDetach)
            panicked = false  // 标记为正常退出
            return
        }
        
        // ========== 6. 解锁 ==========
        c.unlock(processing)
        
        // ========== 7. 双重检查（防止竞态） ==========
        // 场景：解锁瞬间，Poller 检测到关闭
        if c.status(closing) != 0 && c.lock(processing) {
            // Poller 获取锁失败，我们帮它执行 closeCallback
            c.closeCallback(false, false)
            panicked = false
            return
        }
        
        // ========== 8. 检查新数据（防止遗漏） ==========
        // 场景：解锁瞬间，Poller 写入了新数据
        if onRequest != nil && c.Reader().Len() > 0 && c.lock(processing) {
            goto START  // 重新处理
        }
        
        // ========== 9. 正常退出 ==========
        panicked = false
    }
    
    // ========== 10. 提交任务到协程池 ==========
    runner.RunTask(c.ctx, task)
    return true
}
```

### 关键设计点详解

#### 1. processing 锁：保证串行

```go
if !c.lock(processing) {
    return false
}
```

- 同一时刻只有一个 goroutine 可以执行 OnRequest
- 避免了数据竞态和状态混乱
- 如果获取失败，说明已有任务在运行，直接返回

#### 2. Panic 恢复：防止资源泄漏

```go
panicked := true
defer func() {
    if !panicked {
        return
    }
    // Panic 处理逻辑
}()
// ... 正常逻辑 ...
panicked = false
```

**设计精妙之处**：
- 默认假设会 panic（`panicked = true`）
- 只有正常执行到末尾才设置 `panicked = false`
- defer 中检查 `panicked`，如果为 true 说明发生了 panic
- 即使在 recover() 之前 panic，defer 也会执行

#### 3. 循环处理：避免遗漏数据

```go
for {
    closedBy = c.status(closing)
    if closedBy == user || onRequest == nil || c.Reader().Len() == 0 {
        break
    }
    _ = onRequest(c.ctx, c)
}
```

**为什么要循环**：
- 单次 OnRequest 可能只处理一部分数据
- 如果有多个完整的包，需要全部处理
- 避免频繁的 goroutine 切换开销

**退出条件**：
- 用户主动关闭 → 立即退出
- 没有 OnRequest 回调 → 退出（不应该发生）
- 没有数据了 → 正常退出

#### 4. 双重检查：防止竞态

```go
c.unlock(processing)

// 双重检查 1：是否被关闭
if c.status(closing) != 0 && c.lock(processing) {
    c.closeCallback(false, false)
    panicked = false
    return
}

// 双重检查 2：是否有新数据
if onRequest != nil && c.Reader().Len() > 0 && c.lock(processing) {
    goto START
}
```

**竞态场景 1**：
```
Time 1: onProcess 解锁
Time 2: Poller 检测到 HUP，尝试获取锁失败
Time 3: onProcess 退出
Time 4: closeCallback 永远不会被调用 → 资源泄漏
```

**解决方案**：
- onProcess 解锁后，再次检查 `closing` 状态
- 如果发现被关闭了，重新获取锁并执行 closeCallback
- 这样即使 Poller 获取锁失败，也能保证 closeCallback 被调用

**竞态场景 2**：
```
Time 1: onProcess 处理完数据，准备退出
Time 2: Poller 接收到新数据
Time 3: Poller 调用 triggerRead，但 goroutine 已经退出
Time 4: 新数据无人处理
```

**解决方案**：
- onProcess 解锁后，再次检查是否有新数据
- 如果有，重新获取锁并 goto START

#### 5. goto START：高效的循环重入

```go
START:
    // 执行 OnRequest
    if onRequest != nil && c.Reader().Len() > 0 {
        _ = onRequest(c.ctx, c)
    }
    // ...
    goto START
```

**为什么使用 goto**：
- 避免递归调用（栈溢出风险）
- 避免创建新的 goroutine（开销大）
- 在同一个任务上下文中继续处理

---

## 🔌 OnDisconnect：连接断开时的清理

### 执行时机

OnDisconnect 在以下情况被触发：
1. **OnHup 中**：Poller 检测到对端关闭
2. **onProcess 中**：OnConnect 执行后发现连接已关闭

### onDisconnect 方法

```go
func (c *connection) onDisconnect() {
    onDisconnect, _ := c.onDisconnectCallback.Load().(OnDisconnect)
    if onDisconnect == nil {
        return
    }
    
    onConnect, _ := c.onConnectCallback.Load().(OnConnect)
    
    // 情况 1：没有 OnConnect，可以直接执行
    if onConnect == nil {
        c.setState(connStateDisconnected)
        onDisconnect(c.ctx, c)
        return
    }
    
    // 情况 2：有 OnConnect，需要等待其完成
    // 检查状态是否不是 None（OnConnect 已完成）
    if c.getState() != connStateNone && c.lock(connecting) {
        // 尝试修改状态为 Disconnected
        if c.changeState(connStateConnected, connStateDisconnected) {
            onDisconnect(c.ctx, c)
        }
        c.unlock(connecting)
        return
    }
    
    // 情况 3：OnConnect 正在执行，返回
    // 让 OnConnect 在 onProcess 中帮我们调用 OnDisconnect
}
```

**复杂的逻辑分析**：

#### 情况 1：没有 OnConnect
```
简单情况，直接执行
```

#### 情况 2：OnConnect 已完成
```
Time 1: OnConnect 完成，state = Connected
Time 2: Poller 检测到 HUP
Time 3: onDisconnect 获取 connecting 锁
Time 4: 修改 state = Disconnected
Time 5: 执行 OnDisconnect
```

#### 情况 3：OnConnect 正在执行
```
Time 1: OnConnect 正在执行（持有 connecting 锁）
Time 2: Poller 检测到 HUP
Time 3: onDisconnect 尝试获取 connecting 锁失败
Time 4: onDisconnect 返回
Time 5: OnConnect 在 onProcess 中检测到连接已关闭
Time 6: OnConnect 帮忙执行 OnDisconnect
```

**关键点**：
- 通过 `connecting` 锁协调 OnConnect 和 OnDisconnect
- 保证 OnDisconnect 不会在 OnConnect 之前执行
- 保证 OnDisconnect 只执行一次（通过 `changeState`）

---

## 🎣 CloseCallback：连接关闭后的最终清理

### 添加 CloseCallback

```go
func (c *connection) AddCloseCallback(callback CloseCallback) error {
    if callback == nil {
        return nil
    }
    
    // 创建新节点
    cb := &callbackNode{}
    cb.fn = callback
    
    // 插入到链表头部
    if pre := c.closeCallbacks.Load(); pre != nil {
        cb.pre = pre.(*callbackNode)
    }
    
    // 原子更新
    c.closeCallbacks.Store(cb)
    return nil
}
```

**链表结构**：
```
最新 → callback3 → callback2 → callback1 → nil
```

### 执行 CloseCallback

```go
func (c *connection) closeCallback(needLock, needDetach bool) (err error) {
    // 1. 获取 processing 锁（如果需要）
    if needLock && !c.lock(processing) {
        return nil  // 无法获取锁，说明有任务在运行，它会负责调用
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
        callback.fn(c)  // 忽略错误
    }
    
    // 4. 关闭缓冲区
    c.closeBuffer()
    
    return nil
}
```

**执行顺序**：
```
callback3 → callback2 → callback1
```

**逆序的原因**：
- 类似于 defer 的语义
- 最后添加的（通常是最内层的资源）最先清理
- 符合 RAII 的资源管理习惯

### CloseCallback 的典型用途

```go
// 示例 1：清理会话资源
conn.AddCloseCallback(func(conn Connection) error {
    session := getSession(conn)
    session.Close()
    return nil
})

// 示例 2：统计连接时长
startTime := time.Now()
conn.AddCloseCallback(func(conn Connection) error {
    duration := time.Since(startTime)
    metrics.RecordConnDuration(duration)
    return nil
})

// 示例 3：通知其他模块
conn.AddCloseCallback(func(conn Connection) error {
    connManager.Remove(conn.RemoteAddr().String())
    return nil
})
```

---

## 🔍 事件触发机制

### triggerRead：唤醒等待的 Reader

```go
func (c *connection) triggerRead(err error) {
    select {
    case c.readTrigger <- err:  // 非阻塞发送
    default:
        // channel 满了或没有接收者，忽略
    }
}
```

**调用场景**：
1. **inputAck 中**：新数据到达，数据量满足 `waitReadSize`
2. **onHup 中**：连接关闭，唤醒并返回错误
3. **onClose 中**：用户主动关闭

**为什么非阻塞**：
- 可能没有 goroutine 在等待（没有调用 `Next` 等方法）
- 不能阻塞 Poller 的 goroutine

### triggerWrite：唤醒等待的 Writer

```go
func (c *connection) triggerWrite(err error) {
    select {
    case c.writeTrigger <- err:
    default:
    }
}
```

**调用场景**：
1. **rw2r 中**：outputBuffer 清空，写入完成
2. **onHup 中**：连接关闭
3. **onClose 中**：用户主动关闭

---

## 🛡️ 并发安全保证

### 使用的同步机制

1. **atomic.Value**：存储回调函数
   ```go
   c.onRequestCallback.Store(onRequest)
   onRequest, _ := c.onRequestCallback.Load().(OnRequest)
   ```

2. **locker 位标志**：控制执行流程
   ```go
   c.lock(processing)
   c.lock(connecting)
   c.lock(flushing)
   ```

3. **atomic CAS 状态**：连接状态转换
   ```go
   c.changeState(connStateNone, connStateConnected)
   ```

4. **channel**：唤醒阻塞的 goroutine
   ```go
   c.triggerRead(err)
   ```

### 关键的竞态场景及解决方案

#### 场景 1：OnRequest 并发执行

**问题**：
- 多次触发 OnRequest，可能导致并发执行
- 数据竞态，状态混乱

**解决**：
- `processing` 锁保证串行执行
- `onProcess` 通过 CAS 获取锁

#### 场景 2：Poller 和用户同时关闭

**问题**：
- Poller 检测到 HUP
- 用户调用 Close()
- closeCallback 可能被执行两次

**解决**：
- `closeBy(who)` 通过 CAS 操作标记关闭原因
- 只有第一个成功的会执行后续逻辑

#### 场景 3：OnConnect 和 OnDisconnect 的顺序

**问题**：
- OnConnect 正在执行
- Poller 检测到 HUP，触发 OnDisconnect
- OnDisconnect 不能在 OnConnect 之前执行

**解决**：
- `connecting` 锁保护 OnConnect
- onDisconnect 检查状态和锁
- 如果 OnConnect 正在执行，返回并让 OnConnect 代劳

---

## 📊 性能优化技巧

### 1. 批量处理数据

```go
func MyOnRequest(ctx context.Context, conn Connection) error {
    reader := conn.Reader()
    defer reader.Release()
    
    // ✅ 好：循环处理所有数据
    for reader.Len() > 0 {
        processPacket(reader)
    }
    
    return nil
}
```

### 2. 避免频繁的 goroutine 创建

- `onProcess` 使用 `goto START` 而不是递归
- 任务提交到协程池 (`runner.RunTask`)

### 3. 精确的事件控制

- 数据发送完毕后，立即切换为 `PollReadable`
- 避免虚假的 POLLOUT 唤醒

### 4. 双重检查减少竞态窗口

- 解锁后再次检查状态
- 避免遗漏事件

---

## 🔍 总结

Connection 的事件处理机制体现了 Netpoll 的设计精髓：

1. **精心设计的状态机**
   - 保证回调的执行顺序
   - 防止重复执行

2. **多层次的并发控制**
   - atomic.Value、locker、CAS
   - 每种机制各司其职

3. **Panic 安全的任务执行**
   - defer + recover 机制
   - 保证资源不泄漏

4. **智能的双重检查**
   - 消除竞态窗口
   - 保证事件不遗漏

5. **高效的循环处理**
   - goto 避免递归
   - 批量处理减少唤醒

理解这些机制，才能在使用 Netpoll 时避免踩坑，并充分发挥其性能优势。

