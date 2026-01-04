## 第一部分：初始化与核心结构解析

```go
func openDefaultPoll() (*defaultPoll, error) {
    poll := new(defaultPoll)
    poll.buf = make([]byte, 8) // 用于读取 eventfd 的 8字节 buffer
    
    // 1. 创建 epoll 句柄
    p, err := EpollCreate(0)
    if err != nil { return nil, err }
    poll.fd = p

    // 2. 创建 eventfd (用于唤醒 epoll_wait)
    r0, _, e0 := syscall.Syscall(syscall.SYS_EVENTFD2, 0, 0, 0)
    if e0 != 0 { ... }

    poll.Reset = poll.reset
    poll.Handler = poll.handler
    poll.wop = &FDOperator{FD: int(r0)} // 将 eventfd 封装成 FDOperator

    // 3. 将 eventfd 注册进 epoll，监听可读事件
    if err = poll.Control(poll.wop, PollReadable); err != nil { ... }

    poll.opcache = newOperatorCache() // 初始化对象池
    return poll, nil
}

type defaultPoll struct {
    pollArgs           // 继承 pollArgs (复用内存)
    fd      int        // epoll 的文件描述符
    wop     *FDOperator // "Wake Operator", 包装了 eventfd
    buf     []byte     // 读取 eventfd 用的 buffer
    trigger uint32     // 原子标记，防止频繁触发 eventfd
    m       sync.Map   // 仅在 race detection 下使用，生产环境不关注
    opcache *operatorCache 
    // ...
}
```

这份 `poll_default_linux.go` 是 Netpoll 在 Linux 平台下的核心实现，代码密度很高，逻辑非常精巧。

为了让你能够系统地消化这份代码，我建议将它分为以下**四个部分**进行解析：

1. **第一部分：初始化与核心结构** (`openDefaultPoll`, `defaultPoll`, `pollArgs`)
   - 关注 Poller 对象的创建、资源分配以及 `eventfd` 的建立。
2. **第二部分：控制平面接口** (`Control`, `Trigger`, `Close`)
   - 关注如何注册 FD、修改监听事件以及如何唤醒 Poller。
3. **第三部分：事件循环引擎** (`Wait`)
   - 关注主循环模型、`epoll_wait` 的调用以及动态扩容机制。
4. **第四部分：事件分发逻辑** (`handler`)
   - 这是最复杂的部分，关注读写事件的具体处理、回调执行以及与 Connection 的交互。

------



### 第一部分：初始化与核心结构解析



这一部分是地基。我们需要理解 Netpoll 启动时向操作系统申请了什么资源，以及它在内存中是如何组织数据的。



#### 1. 核心代码段



Go



```
func openDefaultPoll() (*defaultPoll, error) {
    poll := new(defaultPoll)
    poll.buf = make([]byte, 8) // 用于读取 eventfd 的 8字节 buffer
    
    // 1. 创建 epoll 句柄
    p, err := EpollCreate(0)
    if err != nil { return nil, err }
    poll.fd = p

    // 2. 创建 eventfd (用于唤醒 epoll_wait)
    r0, _, e0 := syscall.Syscall(syscall.SYS_EVENTFD2, 0, 0, 0)
    if e0 != 0 { ... }

    poll.Reset = poll.reset
    poll.Handler = poll.handler
    poll.wop = &FDOperator{FD: int(r0)} // 将 eventfd 封装成 FDOperator

    // 3. 将 eventfd 注册进 epoll，监听可读事件
    if err = poll.Control(poll.wop, PollReadable); err != nil { ... }

    poll.opcache = newOperatorCache() // 初始化对象池
    return poll, nil
}

type defaultPoll struct {
    pollArgs           // 继承 pollArgs (复用内存)
    fd      int        // epoll 的文件描述符
    wop     *FDOperator // "Wake Operator", 包装了 eventfd
    buf     []byte     // 读取 eventfd 用的 buffer
    trigger uint32     // 原子标记，防止频繁触发 eventfd
    m       sync.Map   // 仅在 race detection 下使用，生产环境不关注
    opcache *operatorCache 
    // ...
}
```



这一阶段主要完成了三件大事：

##### A. `EpollCreate(0)`：建立监控室

这是 Linux `epoll` 的起点。`poll.fd` 就是这个监控室的 ID。之后所有的 socket 连接都会被添加到这个 ID 下面进行管理。

##### B. `SYS_EVENTFD2`：建立“叫醒服务” (关键点)

Netpoll 在这里通过系统调用创建了一个 **eventfd**。

- **为什么需要它？** `epoll_wait` 是阻塞的。如果没有任何网络数据到来，线程会挂起。此时，如果你想从另一个 goroutine 往 Poller 里注册一个新的连接，或者关闭 Poller，你需要一种机制把正在阻塞的 Poller **“叫醒”**。
- **如何实现？** `eventfd` 是一个专门用于事件通知的文件描述符（本质是一个内存中的 64 位计数器）。往里面写数据，它就变“可读”；读数据，它就变“不可读”。
- **代码体现**： `poll.wop = &FDOperator{FD: int(r0)}`：将这个 `eventfd` 包装起来，称为 `wop` (Wake Operator)。

##### C. Self-Registration：监听自己

```go
poll.Control(poll.wop, PollReadable)
```

这是一个非常聪明的闭环设计。Poller 初始化时，**第一个**注册到 epoll 里的 FD，不是网络连接，而是它自己的 **eventfd (wop)**。 这意味着：一旦有人调用 `Trigger()` 往 `eventfd` 里写数据，`epoll_wait` 就会立刻返回（因为检测到了 `wop` 可读），从而打断阻塞，让 Poller 有机会处理其他任务。

##### D. 内存复用结构 `pollArgs`

注意 `defaultPoll` 结构体中嵌入了 `pollArgs`。

```go
type pollArgs struct {
    size     int
    caps     int
    events   []epollevent // 复用数组，用于接收 epoll_wait 返回的事件
    barriers []barrier    // 复用结构，用于 LinkBuffer 的内存屏障
    // ...
}
```

- **目的**：为了减少 GC。
- `events` 切片是在 Poller 生命周期内一直复用的。每次 `epoll_wait` 返回时，内核直接把数据填入这块内存，避免了每次循环都 `make([]epollevent, ...)`。